// Package api wires HTTP routes to the store, storage and auth layers.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/YLHhhh123/HollowTone-server/internal/auth"
	"github.com/YLHhhh123/HollowTone-server/internal/config"
	"github.com/YLHhhh123/HollowTone-server/internal/storage"
	"github.com/YLHhhh123/HollowTone-server/internal/store"
)

// maxAuthAttempts is how many failed credential attempts one client may make
// per authWindow before the API answers 429.
const (
	maxAuthAttempts = 10
	authWindow      = 5 * time.Minute
)

// Server bundles the dependencies of the HTTP API.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	storage *storage.Store
	tokens  *auth.Manager
	start   time.Time
	// authLimiter throttles credential endpoints per client address.
	authLimiter *limiter
}

// New constructs the API server.
func New(cfg *config.Config, st *store.Store, stor *storage.Store) *Server {
	return &Server{
		cfg:         cfg,
		store:       st,
		storage:     stor,
		tokens:      auth.New(cfg.JWTSecret, cfg.TokenTTL),
		start:       time.Now(),
		authLimiter: newLimiter(maxAuthAttempts, authWindow),
	}
}

// Handler builds the full routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public endpoints.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
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
	return logging(cors(s.cfg.CORSOrigins, mux))
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

// readJSON decodes a JSON request body with a size cap. Unknown fields are
// ignored on purpose: the client evolves faster than the server, and rejecting
// extra fields would turn a forward-compatible client into a hard failure.
func readJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	return dec.Decode(dst)
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

// throttleApply enforces the credential-attempt budget for this client,
// answering 429 with Retry-After when it is exhausted.
func (s *Server) throttleApply(w http.ResponseWriter, r *http.Request, scope string) (string, bool) {
	key := scope + "|" + clientIP(r, s.cfg.TrustProxy)
	ok, wait := s.authLimiter.allow(key)
	if ok {
		return key, true
	}
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, "too many attempts, retry later")
	return key, false
}
