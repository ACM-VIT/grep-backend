package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Storage writes an uploaded file to UPLOAD_DIR and reports the URL it will be
// reachable at.
//
// There is deliberately no S3, GCS or R2 client here. The admin can paste a
// bucket URL straight into the PDF field, which is all any of those need from
// this service; and when a file is uploaded here instead, pointing
// PUBLIC_FILES_BASE_URL at a bucket or CDN that mirrors UPLOAD_DIR gives the
// same result without three vendor SDKs to keep current. README.md, "Where the
// files live", covers both routes.
type Storage struct {
	dir     string
	baseURL string
	maxSize int64
}

func NewStorage(cfg *Config) (*Storage, error) {
	if err := os.MkdirAll(cfg.UploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir: %w", err)
	}
	return &Storage{
		dir:     cfg.UploadDir,
		baseURL: cfg.PublicFilesBaseURL,
		maxSize: cfg.MaxUploadBytes,
	}, nil
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// safeName reduces an uploaded filename to something that cannot escape the
// upload directory or surprise a filesystem: no separators, no traversal, no
// leading dot, and a length a filesystem will accept.
func safeName(original string) string {
	name := filepath.Base(strings.TrimSpace(original))
	name = unsafeName.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		name = "upload"
	}
	if len(name) > 80 {
		// Trim the stem, keep the extension - a truncated ".pdf" helps nobody.
		ext := filepath.Ext(name)
		if len(ext) > 10 {
			ext = ""
		}
		name = name[:80-len(ext)] + ext
	}
	return name
}

// Save writes the upload and returns its public URL.
//
// Every file gets a random prefix. Two editions uploading "grep.pdf" must not
// collide, and a guessable name would let anyone enumerate what has been
// uploaded but not yet published.
func (s *Storage) Save(file multipart.File, header *multipart.FileHeader) (string, string, error) {
	if header.Size > s.maxSize {
		return "", "", fmt.Errorf("file is %.1f MB; the limit is %d MB",
			float64(header.Size)/(1<<20), s.maxSize>>20)
	}

	var prefix [8]byte
	if _, err := rand.Read(prefix[:]); err != nil {
		return "", "", fmt.Errorf("generate name: %w", err)
	}
	name := hex.EncodeToString(prefix[:]) + "-" + safeName(header.Filename)
	path := filepath.Join(s.dir, name)

	// O_EXCL so a collision fails loudly rather than overwriting.
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("create file: %w", err)
	}

	// LimitReader guards the write itself: header.Size is what the client
	// claimed, and the check above trusts it only far enough to fail early.
	written, err := io.Copy(out, io.LimitReader(file, s.maxSize+1))
	if err == nil && written > s.maxSize {
		err = fmt.Errorf("file exceeds the %d MB limit", s.maxSize>>20)
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", "", err
	}

	return name, s.baseURL + "/" + name, nil
}
