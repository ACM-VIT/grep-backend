package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Edition mirrors the shape the website already reads in
// `src/lib/types.ts`. The website's own v0 and v1 stay compiled into the
// frontend as a fallback; anything created here is merged over them by slug.
//
// Sections are kept as raw JSON rather than modelled block by block. The block
// union is the website's business, it changes when the print design changes,
// and re-declaring all seventeen variants here would only create a second place
// to keep in step. The backend stores and serves them; the frontend types them.
type Edition struct {
	Slug        string `json:"slug"`
	Number      int    `json:"number"`
	Name        string `json:"name,omitempty"`
	HeaderLabel string `json:"headerLabel"`
	FooterLabel string `json:"footerLabel"`
	Dateline    string `json:"dateline"`
	ISODate     string `json:"isoDate"`
	Tagline     string `json:"tagline"`
	Blurb       string `json:"blurb"`
	Pages       int    `json:"pages"`
	PDF         string `json:"pdf"`
	Cover       string `json:"cover"`

	// "pdf" carries a cover and a download and nothing else; "full" also
	// carries sections and gets the reader. An edition with no sections is a
	// pdf edition whatever it claims, and Normalise enforces that.
	Kind   string `json:"kind"`
	Status string `json:"status"`

	// Raw section JSON, passed through untouched. `json.RawMessage` would be
	// tidier but makes the zero value `null` rather than `[]`, which the
	// frontend would have to guard on every read.
	Sections []any `json:"sections"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

const (
	KindPDF  = "pdf"
	KindFull = "full"

	StatusDraft     = "draft"
	StatusPublished = "published"
)

// Subscriber is one address captured by the website's form. The website posts
// them here so the admin has somewhere to read them; the mailing-list provider
// stays whatever the website is configured with.
type Subscriber struct {
	Email  string    `json:"email"`
	Source string    `json:"source"`
	Time   time.Time `json:"ts"`
}

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	isoPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Normalise trims the edition into a valid, self-consistent record, or explains
// why it cannot. It is the only validation in the service: everything that
// writes an edition goes through it, so there is one place to look.
func (e *Edition) Normalise() error {
	e.Slug = strings.TrimSpace(strings.ToLower(e.Slug))
	e.Name = strings.TrimSpace(e.Name)
	e.HeaderLabel = strings.TrimSpace(e.HeaderLabel)
	e.FooterLabel = strings.TrimSpace(e.FooterLabel)
	e.Dateline = strings.TrimSpace(e.Dateline)
	e.ISODate = strings.TrimSpace(e.ISODate)
	e.Tagline = strings.TrimSpace(e.Tagline)
	e.Blurb = strings.TrimSpace(e.Blurb)
	e.PDF = strings.TrimSpace(e.PDF)

	if !slugPattern.MatchString(e.Slug) {
		return errors.New("slug must be lowercase words joined by single hyphens, e.g. grep-v2")
	}
	if e.Number < 0 {
		return errors.New("number must be zero or greater")
	}
	if e.Dateline == "" {
		return errors.New("dateline is required, e.g. August 2026")
	}
	if !isoPattern.MatchString(e.ISODate) {
		return errors.New("isoDate must look like 2026-08-01")
	}
	if _, err := time.Parse("2006-01-02", e.ISODate); err != nil {
		return errors.New("isoDate is not a real date")
	}
	if e.PDF == "" {
		return errors.New("a PDF link is required - upload one or paste a bucket URL")
	}
	if !strings.HasPrefix(e.PDF, "http://") &&
		!strings.HasPrefix(e.PDF, "https://") &&
		!strings.HasPrefix(e.PDF, "/") {
		return errors.New("the PDF link must be an http(s) URL or a site-absolute path")
	}
	if e.Pages < 0 {
		return errors.New("pages must be zero or greater")
	}

	// Everything below has a sensible answer, so fill it in rather than
	// refusing a form that was otherwise complete.
	if e.Cover != "brick" {
		e.Cover = "keyboard"
	}
	if e.Status != StatusPublished {
		e.Status = StatusDraft
	}
	if e.Sections == nil {
		e.Sections = []any{}
	}
	// The kind follows the content, not the claim: an edition with no sections
	// has nothing for the reader to show whatever it is labelled.
	if len(e.Sections) == 0 {
		e.Kind = KindPDF
	} else {
		e.Kind = KindFull
	}
	if e.HeaderLabel == "" {
		e.HeaderLabel = fmt.Sprintf("grep v%d", e.Number)
	}
	if e.FooterLabel == "" {
		e.FooterLabel = e.HeaderLabel
	}
	if e.Tagline == "" {
		e.Tagline = "Your search query for ACM-VIT updates."
	}
	if e.Blurb == "" {
		e.Blurb = e.Tagline
	}
	return nil
}
