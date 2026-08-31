package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Store keeps the editions and the captured subscribers.
//
// Two JSON files under DATA_DIR, held in memory and rewritten whole on every
// change. A newsletter publishes a few times a year to a handful of editors, so
// a database would be a dependency to install, back up and migrate in exchange
// for concurrency this will never need. Writes go through a temp file and an
// atomic rename, so a crash mid-write leaves the previous file intact rather
// than a truncated one.
type Store struct {
	mu          sync.RWMutex
	dir         string
	editions    []Edition
	subscribers []Subscriber
}

const (
	editionsFile    = "editions.json"
	subscribersFile = "subscribers.json"
)

var ErrNotFound = errors.New("not found")

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{dir: dir, editions: []Edition{}, subscribers: []Subscriber{}}
	if err := readJSON(filepath.Join(dir, editionsFile), &s.editions); err != nil {
		return nil, fmt.Errorf("read %s: %w", editionsFile, err)
	}
	if err := readJSON(filepath.Join(dir, subscribersFile), &s.subscribers); err != nil {
		return nil, fmt.Errorf("read %s: %w", subscribersFile, err)
	}
	return s, nil
}

// readJSON loads a file into out. A file that is not there yet is not an error:
// a first run has no data, and out keeps its empty value.
func readJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// writeJSON writes through a temp file in the same directory and renames it
// over the target, so a reader never sees a half-written file.
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below has succeeded

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *Store) persistEditions() error {
	return writeJSON(filepath.Join(s.dir, editionsFile), s.editions)
}

// sortEditions puts the newest first, which is the order every surface of the
// website reads in.
func sortEditions(list []Edition) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Number > list[j].Number })
}

// Editions returns every edition, drafts included. Admin use.
func (s *Store) Editions() []Edition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Edition(nil), s.editions...)
	sortEditions(out)
	return out
}

// PublishedEditions returns only what the public site should show.
func (s *Store) PublishedEditions() []Edition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Edition, 0, len(s.editions))
	for _, edition := range s.editions {
		if edition.Status == StatusPublished {
			out = append(out, edition)
		}
	}
	sortEditions(out)
	return out
}

func (s *Store) Edition(slug string) (Edition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, edition := range s.editions {
		if edition.Slug == slug {
			return edition, nil
		}
	}
	return Edition{}, ErrNotFound
}

// SaveEdition creates or replaces an edition by slug. The caller has already
// run Normalise, so this only deals with placement and timestamps.
func (s *Store) SaveEdition(edition Edition, actor string, allowCreate bool) (Edition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	edition.UpdatedAt = now
	edition.UpdatedBy = actor

	for i, existing := range s.editions {
		if existing.Slug == edition.Slug {
			edition.CreatedAt = existing.CreatedAt
			s.editions[i] = edition
			if err := s.persistEditions(); err != nil {
				s.editions[i] = existing // put the in-memory copy back
				return Edition{}, err
			}
			return edition, nil
		}
	}

	if !allowCreate {
		return Edition{}, ErrNotFound
	}
	edition.CreatedAt = now
	s.editions = append(s.editions, edition)
	if err := s.persistEditions(); err != nil {
		s.editions = s.editions[:len(s.editions)-1]
		return Edition{}, err
	}
	return edition, nil
}

func (s *Store) DeleteEdition(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, edition := range s.editions {
		if edition.Slug != slug {
			continue
		}
		removed := s.editions
		s.editions = append(append([]Edition(nil), s.editions[:i]...), s.editions[i+1:]...)
		if err := s.persistEditions(); err != nil {
			s.editions = removed
			return err
		}
		return nil
	}
	return ErrNotFound
}

// AddSubscriber records an address, ignoring one already held. The website
// still forwards to whatever mailing-list provider it is configured with; this
// is only so the admin has a list to look at.
func (s *Store) AddSubscriber(sub Subscriber) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.subscribers {
		if existing.Email == sub.Email {
			return nil
		}
	}
	s.subscribers = append(s.subscribers, sub)
	if err := writeJSON(filepath.Join(s.dir, subscribersFile), s.subscribers); err != nil {
		s.subscribers = s.subscribers[:len(s.subscribers)-1]
		return err
	}
	return nil
}

// Subscribers returns the newest first.
func (s *Store) Subscribers() []Subscriber {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Subscriber(nil), s.subscribers...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}
