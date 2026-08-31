package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/api/idtoken"
)

// Admin is the caller of an admin route, once their token has checked out.
type Admin struct {
	Email   string `json:"email"`
	Name    string `json:"name,omitempty"`
	Picture string `json:"picture,omitempty"`
}

// Authenticator turns the Google ID token on a request into an Admin.
//
// There is no session of our own: the browser holds a Google ID token and sends
// it on every call, and this verifies it each time. Google's library caches the
// signing keys, so verification is local after the first call - no round trip
// per request, and nothing to store, expire or revoke on this side. The tokens
// last an hour, which the admin page handles by asking Google for a fresh one.
type Authenticator struct {
	cfg      *Config
	validate func(ctx context.Context, token, audience string) (*idtoken.Payload, error)
}

func NewAuthenticator(cfg *Config) *Authenticator {
	return &Authenticator{cfg: cfg, validate: idtoken.Validate}
}

var errNoToken = errors.New("missing bearer token")

func bearer(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", errNoToken
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", errors.New("authorization header must be a bearer token")
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", errNoToken
	}
	return token, nil
}

// Authenticate verifies the token and checks the address against the allowlist.
//
// Both failures are reported the same way to the caller - see requireAdmin -
// so that a stranger cannot use the difference to learn who is on the list.
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (*Admin, error) {
	token, err := bearer(r)
	if err != nil {
		return nil, err
	}

	payload, err := a.validate(ctx, token, a.cfg.GoogleClientID)
	if err != nil {
		return nil, fmt.Errorf("token rejected: %w", err)
	}

	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return nil, errors.New("token carries no email claim")
	}
	// An unverified address on a Google account is one the holder has not
	// proved they control, so it is not something to match an allowlist on.
	if verified, ok := payload.Claims["email_verified"].(bool); !ok || !verified {
		return nil, errors.New("email address is not verified with Google")
	}
	if !a.cfg.IsAdmin(email) {
		return nil, fmt.Errorf("%s is not on the admin list", email)
	}

	name, _ := payload.Claims["name"].(string)
	picture, _ := payload.Claims["picture"].(string)
	return &Admin{Email: strings.ToLower(email), Name: name, Picture: picture}, nil
}
