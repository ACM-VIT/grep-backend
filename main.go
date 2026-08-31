package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// grep-backend is the admin service behind grep.acmvit.in.
//
// It does three things: it holds the editions the website reads, it lets an
// allowlisted Google account create and edit them, and it takes PDF uploads.
// Routing is the standard library's - Go's ServeMux has matched on method and
// path pattern since 1.22, so there is no router dependency here.
type Server struct {
	cfg     *Config
	store   *Store
	storage *Storage
	auth    *Authenticator
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[grep] ")

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	store, err := NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	storage, err := NewStorage(cfg)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	srv := &Server{cfg: cfg, store: store, storage: storage, auth: NewAuthenticator(cfg)}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous: an upload of a print edition over a slow connection is a
		// legitimate long request.
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// Shut down on a signal rather than being killed mid-write, so an
	// in-flight edition save finishes and lands on disk.
	done := make(chan struct{})
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		log.Print("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		close(done)
	}()

	log.Printf("listening on %s", cfg.Addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	<-done
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// --- public: what the website reads ---
	mux.HandleFunc("GET /v1/editions", s.handleListPublished)
	mux.HandleFunc("GET /v1/editions/{slug}", s.handleGetPublished)
	mux.HandleFunc("POST /v1/subscribe", s.handleSubscribe)

	// --- admin: everything below requires an allowlisted Google account ---
	mux.Handle("GET /v1/admin/me", s.requireAdmin(s.handleMe))
	mux.Handle("GET /v1/admin/editions", s.requireAdmin(s.handleAdminList))
	mux.Handle("GET /v1/admin/editions/{slug}", s.requireAdmin(s.handleAdminGet))
	mux.Handle("POST /v1/admin/editions", s.requireAdmin(s.handleCreate))
	mux.Handle("PUT /v1/admin/editions/{slug}", s.requireAdmin(s.handleUpdate))
	mux.Handle("DELETE /v1/admin/editions/{slug}", s.requireAdmin(s.handleDelete))
	mux.Handle("POST /v1/admin/uploads", s.requireAdmin(s.handleUpload))
	mux.Handle("GET /v1/admin/subscribers", s.requireAdmin(s.handleSubscribers))

	// Uploaded files. FileServer resolves and cleans the path, so a request for
	// ../../etc/passwd cannot leave the directory.
	mux.Handle("GET /files/", http.StripPrefix("/files/", http.FileServer(http.Dir(s.storage.dir))))

	return s.withCORS(mux)
}

// ---------------------------------------------------------------- responses

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONResponse(w, status, map[string]string{"error": message})
}

// ---------------------------------------------------------------- middleware

func (s *Server) withCORS(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, origin := range s.cfg.AllowedOrigins {
		allowed[strings.TrimRight(origin, "/")] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			// The allowed origin varies by request, so a shared cache must key
			// on it rather than serve one origin's headers to another.
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type adminHandler func(http.ResponseWriter, *http.Request, *Admin)

// requireAdmin verifies the token and hands the identity to the handler.
//
// Both a bad token and a good token belonging to nobody on the list return the
// same 401 with the same message. Distinguishing them would tell a stranger
// which addresses are admins, and the difference is of no use to a real one.
func (s *Server) requireAdmin(next adminHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, err := s.auth.Authenticate(r.Context(), r)
		if err != nil {
			log.Printf("auth refused for %s %s: %v", r.Method, r.URL.Path, err)
			writeError(w, http.StatusUnauthorized, "Sign in with an approved Google account.")
			return
		}
		next(w, r, admin)
	})
}

// decodeJSON reads a request body with a cap, and refuses fields the target
// does not declare so a typo in the admin form is reported rather than dropped.
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

// ---------------------------------------------------------------- public

func (s *Server) handleListPublished(w http.ResponseWriter, r *http.Request) {
	editions := s.store.PublishedEditions()
	// The website re-fetches this on every render, so let a proxy hold it
	// briefly while still letting a publish show up promptly.
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSONResponse(w, http.StatusOK, map[string]any{"editions": editions})
}

func (s *Server) handleGetPublished(w http.ResponseWriter, r *http.Request) {
	edition, err := s.store.Edition(r.PathValue("slug"))
	if err != nil || edition.Status != StatusPublished {
		writeError(w, http.StatusNotFound, "No such edition.")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSONResponse(w, http.StatusOK, edition)
}

// handleSubscribe records an address forwarded by the website's own form
// handler. It is not the mailing list - it is so the admin has a list to read.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email  string `json:"email"`
		Source string `json:"source"`
		TS     string `json:"ts"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read that request.")
		return
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 {
		writeError(w, http.StatusBadRequest, "That email address does not look right.")
		return
	}
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = "website"
	}

	if err := s.store.AddSubscriber(Subscriber{Email: email, Source: source, Time: time.Now().UTC()}); err != nil {
		log.Printf("store subscriber: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not record that address.")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- admin

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, admin *Admin) {
	writeJSONResponse(w, http.StatusOK, admin)
}

func (s *Server) handleAdminList(w http.ResponseWriter, r *http.Request, _ *Admin) {
	writeJSONResponse(w, http.StatusOK, map[string]any{"editions": s.store.Editions()})
}

func (s *Server) handleAdminGet(w http.ResponseWriter, r *http.Request, _ *Admin) {
	edition, err := s.store.Edition(r.PathValue("slug"))
	if err != nil {
		writeError(w, http.StatusNotFound, "No such edition.")
		return
	}
	writeJSONResponse(w, http.StatusOK, edition)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request, admin *Admin) {
	var edition Edition
	if err := decodeJSON(w, r, &edition); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read that edition: "+err.Error())
		return
	}
	if err := edition.Normalise(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.Edition(edition.Slug); err == nil {
		writeError(w, http.StatusConflict, "An edition with that slug already exists.")
		return
	}

	saved, err := s.store.SaveEdition(edition, admin.Email, true)
	if err != nil {
		log.Printf("save edition: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not save that edition.")
		return
	}
	log.Printf("%s created %s (%s, %s)", admin.Email, saved.Slug, saved.Kind, saved.Status)
	writeJSONResponse(w, http.StatusCreated, saved)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request, admin *Admin) {
	slug := r.PathValue("slug")

	var edition Edition
	if err := decodeJSON(w, r, &edition); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read that edition: "+err.Error())
		return
	}
	// The slug in the path is the one that counts: it is what the website
	// links to, and letting the body rename a record silently would break
	// every existing link to it.
	edition.Slug = slug

	if err := edition.Normalise(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	saved, err := s.store.SaveEdition(edition, admin.Email, false)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "No such edition.")
		return
	}
	if err != nil {
		log.Printf("save edition: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not save that edition.")
		return
	}
	log.Printf("%s updated %s (%s, %s)", admin.Email, saved.Slug, saved.Kind, saved.Status)
	writeJSONResponse(w, http.StatusOK, saved)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, admin *Admin) {
	slug := r.PathValue("slug")
	err := s.store.DeleteEdition(slug)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "No such edition.")
		return
	}
	if err != nil {
		log.Printf("delete edition: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not delete that edition.")
		return
	}
	log.Printf("%s deleted %s", admin.Email, slug)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, admin *Admin) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(1<<20))

	// Parse with a small memory budget: anything larger spills to a temp file
	// rather than being held in RAM.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read that upload - it may be over the size limit.")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "No file was attached.")
		return
	}
	defer file.Close()

	name, url, err := s.storage.Save(file, header)
	if err != nil {
		log.Printf("upload from %s: %v", admin.Email, err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("%s uploaded %s", admin.Email, name)
	writeJSONResponse(w, http.StatusCreated, map[string]string{"name": name, "url": url})
}

func (s *Server) handleSubscribers(w http.ResponseWriter, r *http.Request, _ *Admin) {
	subscribers := s.store.Subscribers()
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"count":       len(subscribers),
		"subscribers": subscribers,
	})
}
