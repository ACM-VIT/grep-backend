package main

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// Config is the whole of the service's configuration. Everything comes from the
// environment so there is no config file to keep in step with a deployment, and
// every field is documented in .env.example.
type Config struct {
	Addr string

	// GoogleClientID is the OAuth client the admin page signs in with. It is
	// the audience every ID token is checked against: a token minted for some
	// other application will not verify here even if the address is allowed.
	GoogleClientID string

	// AdminEmails is the allowlist. Nobody outside it can reach an admin
	// route, however valid their Google account is.
	AdminEmails map[string]bool

	// AllowedOrigins are the site origins permitted to call this service from
	// a browser.
	AllowedOrigins []string

	DataDir   string
	UploadDir string

	// PublicBaseURL is this service's own externally reachable base, used to
	// build the URL of an uploaded file.
	PublicBaseURL string

	// PublicFilesBaseURL overrides that for uploads alone. Set it when the
	// upload directory is fronted by a bucket or CDN, so the stored link points
	// at the CDN rather than at this process.
	PublicFilesBaseURL string

	MaxUploadBytes int64
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// splitList reads a comma-separated environment value into trimmed, non-empty
// entries. Trailing commas and stray spaces in a .env file are normal, so they
// are tolerated rather than treated as configuration errors.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		Addr:               ":" + env("PORT", "8080"),
		GoogleClientID:     env("GOOGLE_CLIENT_ID", ""),
		AdminEmails:        map[string]bool{},
		AllowedOrigins:     splitList(env("ALLOWED_ORIGINS", "http://localhost:4321")),
		DataDir:            env("DATA_DIR", "./data"),
		UploadDir:          env("UPLOAD_DIR", "./uploads"),
		PublicBaseURL:      strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		PublicFilesBaseURL: strings.TrimRight(env("PUBLIC_FILES_BASE_URL", ""), "/"),
		MaxUploadBytes:     64 << 20, // 64 MiB - a print edition PDF with images
	}

	if cfg.GoogleClientID == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_ID is required - see README.md, 'Creating the Google sign-in key'")
	}

	for _, address := range splitList(os.Getenv("ADMIN_EMAILS")) {
		cfg.AdminEmails[strings.ToLower(address)] = true
	}
	if len(cfg.AdminEmails) == 0 {
		// Starting with an empty allowlist would serve an admin nobody can
		// reach, which looks like a broken deployment rather than a missing
		// variable. Fail loudly instead.
		return nil, fmt.Errorf("ADMIN_EMAILS is required - a comma-separated list of the Google accounts allowed in")
	}

	if cfg.PublicFilesBaseURL == "" {
		cfg.PublicFilesBaseURL = cfg.PublicBaseURL + "/files"
	}

	log.Printf("config: %d admin address(es), origins %v, files at %s",
		len(cfg.AdminEmails), cfg.AllowedOrigins, cfg.PublicFilesBaseURL)
	return cfg, nil
}

// IsAdmin reports whether an address is on the allowlist. Google addresses are
// case-insensitive, so the comparison is too.
func (c *Config) IsAdmin(email string) bool {
	return c.AdminEmails[strings.ToLower(strings.TrimSpace(email))]
}
