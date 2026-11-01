package api

import (
	"net/http"
	"time"

	"github.com/YLHhhh123/HollowTone-server/internal/version"
)

// handleHealth reports liveness. It answers 503 when the database is
// unreachable so container orchestrators and uptime checks can act on it,
// instead of seeing a green 200 next to a broken dependency.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	status := http.StatusOK
	if err := s.store.DB().Ping(); err != nil {
		dbStatus = "error"
		status = http.StatusServiceUnavailable
	}
	info := version.Current()
	writeJSON(w, status, map[string]any{
		"status":         map[bool]string{true: "ok", false: "degraded"}[status == http.StatusOK],
		"db":             dbStatus,
		"uptimeSeconds":  int64(time.Since(s.start).Seconds()),
		"maxUploadBytes": s.cfg.MaxUploadBytes,
		"chunkSize":      s.cfg.ChunkSize,
		"version":        info.Version,
		"commit":         info.Commit,
	})
}

// handleVersion exposes the build identity so a running deployment can be
// matched against a release artifact without inspecting the binary on disk.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, version.Current())
}
