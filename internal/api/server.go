// Package api wires HTTP routes to the store, storage and auth layers.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luolingy/HollowTone/server/internal/auth"
	"github.com/luolingy/HollowTone/server/internal/config"
	"github.com/luolingy/HollowTone/server/internal/storage"
	"github.com/luolingy/HollowTone/server/internal/store"
)

// Server bundles the dependencies of the HTTP API.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	storage *storage.Store
	tokens  *auth.Manager
	start   time.Time
}

// New constructs the API server.
func New(cfg *config.Config, st *store.Store, stor *storage.Store) *Server {
	return &Server{
		cfg:     cfg,
		store:   st,
		storage: stor,
		tokens:  auth.New(cfg.JWTSecret, cfg.TokenTTL),
		start:   time.Now(),
	}
}

// Handler builds the full routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public endpoints.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)

	// Protected endpoints.
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/v1/me", s.handleMe)

	protected.HandleFunc("GET /api/v1/sync", s.handleSync)

	protected.HandleFunc("PUT /api/v1/tracks/{guid}", s.handleUpsertTrack)
	protected.HandleFunc("GET /api/v1/tracks/{guid}", s.handleGetTrack)
	protected.HandleFunc("POST /api/v1/tracks/{guid}/file", s.handleAttachFile)
	protected.HandleFunc("GET /api/v1/tracks/{guid}/stream", s.handleStreamTrack)
	protected.HandleFunc("GET /api/v1/tracks/{guid}/art", s.handleTrackArt)

	protected.HandleFunc("PUT /api/v1/playlists/{guid}", s.handleUpsertPlaylist)
	protected.HandleFunc("GET /api/v1/playlists/{guid}", s.handleGetPlaylist)

	// Asset uploads (cover art / lyrics as standalone blobs).
	protected.HandleFunc("POST /api/v1/assets", s.handleUploadAsset)
	protected.HandleFunc("GET /api/v1/assets/{key}", s.handleGetAsset)

	// Resumable chunked uploads.
	protected.HandleFunc("POST /api/v1/uploads/{uploadId}/init", s.handleUploadInit)
	protected.HandleFunc("PUT /api/v1/uploads/{uploadId}/chunks/{index}", s.handleUploadChunk)
	protected.HandleFunc("GET /api/v1/uploads/{uploadId}", s.handleUploadStatus)
	protected.HandleFunc("POST /api/v1/uploads/{uploadId}/finalize", s.handleUploadFinalize)

	mux.Handle("/api/v1/", s.tokens.Middleware(protected))
	return logging(mux)
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, sw.status, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// userID extracts the authenticated user, or writes 401 and returns false.
func (s *Server) userID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := auth.UserID(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return 0, false
	}
	return id, true
}

func parseSince(r *http.Request) int64 {
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// handleConflict writes the standard 409 response carrying the current row.
func writeConflict(w http.ResponseWriter, current any) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":   "conflict",
		"current": current,
	})
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// trimPath strips a leading slash for key lookups.
func trimPath(p string) string { return strings.TrimPrefix(p, "/") }
