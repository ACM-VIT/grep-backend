package main

import (
	"os"
	"path/filepath"
	"testing"
)

func validEdition() Edition {
	return Edition{
		Slug:     "grep-v2",
		Number:   2,
		Dateline: "March 2027",
		ISODate:  "2027-03-01",
		PDF:      "https://example.com/grep-v2.pdf",
		Status:   StatusPublished,
	}
}

func TestNormaliseFillsDefaults(t *testing.T) {
	e := validEdition()
	if err := e.Normalise(); err != nil {
		t.Fatalf("valid edition rejected: %v", err)
	}
	if e.Kind != KindPDF {
		t.Errorf("an edition with no sections should be a pdf edition, got %q", e.Kind)
	}
	if e.HeaderLabel != "grep v2" || e.FooterLabel != "grep v2" {
		t.Errorf("labels not defaulted: %q / %q", e.HeaderLabel, e.FooterLabel)
	}
	if e.Cover != "keyboard" {
		t.Errorf("cover not defaulted, got %q", e.Cover)
	}
	if e.Sections == nil {
		t.Error("sections should default to an empty slice, not nil")
	}
}

func TestNormaliseKindFollowsSections(t *testing.T) {
	e := validEdition()
	e.Kind = KindPDF // claims pdf, but carries sections
	e.Sections = []any{map[string]any{"id": "intro"}}
	if err := e.Normalise(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Kind != KindFull {
		t.Errorf("an edition with sections must be full, got %q", e.Kind)
	}
}

func TestNormaliseRejects(t *testing.T) {
	cases := map[string]func(*Edition){
		"empty slug":       func(e *Edition) { e.Slug = "" },
		"spaced slug":      func(e *Edition) { e.Slug = "grep v2" },
		"uppercase kept":   func(e *Edition) { e.Slug = "GREP--V2" },
		"missing dateline": func(e *Edition) { e.Dateline = "" },
		"bad iso date":     func(e *Edition) { e.ISODate = "March 2027" },
		"impossible date":  func(e *Edition) { e.ISODate = "2027-02-31" },
		"missing pdf":      func(e *Edition) { e.PDF = "" },
		"relative pdf":     func(e *Edition) { e.PDF = "grep-v2.pdf" },
		"negative number":  func(e *Edition) { e.Number = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEdition()
			mutate(&e)
			if err := e.Normalise(); err == nil {
				t.Errorf("expected %s to be rejected", name)
			}
		})
	}
}

func TestNormaliseLowercasesSlug(t *testing.T) {
	e := validEdition()
	e.Slug = "  GREP-V2  "
	if err := e.Normalise(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Slug != "grep-v2" {
		t.Errorf("slug not normalised, got %q", e.Slug)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	e := validEdition()
	if err := e.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if _, err := store.SaveEdition(e, "editor@acmvit.in", true); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A draft must not reach the public list.
	draft := validEdition()
	draft.Slug = "grep-v3"
	draft.Number = 3
	draft.Status = StatusDraft
	if err := draft.Normalise(); err != nil {
		t.Fatalf("normalise draft: %v", err)
	}
	if _, err := store.SaveEdition(draft, "editor@acmvit.in", true); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	if got := len(store.Editions()); got != 2 {
		t.Errorf("admin list should hold both, got %d", got)
	}
	published := store.PublishedEditions()
	if len(published) != 1 || published[0].Slug != "grep-v2" {
		t.Errorf("published list wrong: %+v", published)
	}

	// Newest first.
	all := store.Editions()
	if all[0].Number != 3 {
		t.Errorf("editions should sort newest first, got %d first", all[0].Number)
	}

	// Reopening must see what was written.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(reopened.Editions()); got != 2 {
		t.Errorf("after reopen expected 2 editions, got %d", got)
	}

	// Update must not create, and must keep the created timestamp.
	missing := validEdition()
	missing.Slug = "nope"
	_ = missing.Normalise()
	if _, err := store.SaveEdition(missing, "editor@acmvit.in", false); err == nil {
		t.Error("update of a missing edition should fail")
	}

	if err := store.DeleteEdition("grep-v3"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteEdition("grep-v3"); err == nil {
		t.Error("deleting twice should report not found")
	}
	if got := len(store.Editions()); got != 1 {
		t.Errorf("after delete expected 1 edition, got %d", got)
	}
}

func TestStoreSubscribersAreDeduped(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	for range 3 {
		if err := store.AddSubscriber(Subscriber{Email: "reader@example.com", Source: "website"}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if got := len(store.Subscribers()); got != 1 {
		t.Errorf("expected the address to be held once, got %d", got)
	}
}

func TestStoreWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	e := validEdition()
	_ = e.Normalise()
	if _, err := store.SaveEdition(e, "editor@acmvit.in", true); err != nil {
		t.Fatalf("save: %v", err)
	}

	// No temp files should survive a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			t.Errorf("unexpected leftover file %q", entry.Name())
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"grep v2.pdf":          "grep-v2.pdf",
		"../../etc/passwd":     "passwd",
		"  spaced  .pdf":       "spaced-.pdf",
		"":                     "upload",
		"...":                  "upload",
		"/absolute/path/a.pdf": "a.pdf",
		"weird\\name?.pdf":     "weird-name-.pdf",
	}
	for input, want := range cases {
		if got := safeName(input); got != want {
			t.Errorf("safeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSafeNameTruncatesButKeepsExtension(t *testing.T) {
	long := ""
	for range 200 {
		long += "a"
	}
	got := safeName(long + ".pdf")
	if len(got) > 80 {
		t.Errorf("name not truncated: %d chars", len(got))
	}
	if filepath.Ext(got) != ".pdf" {
		t.Errorf("extension lost: %q", got)
	}
}
