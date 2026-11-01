package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/YLHhhh123/HollowTone-server/internal/storage"
)

// maxChunkCount bounds the number of chunks a single resumable upload may
// declare, mirroring the storage layer's own guard.
const maxChunkCount = 100_000

// handleAttachFile accepts a whole-file upload for a track (simple path) and
// binds the resulting object to it. Large files should use the chunked upload
// endpoints instead; this endpoint is capped by MaxUploadBytes.
func (s *Server) handleAttachFile(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")

	// Confirm the track exists and belongs to the user before accepting bytes.
	if _, err := s.store.GetTrack(uid, guid); err != nil {
		writeErr(w, http.StatusNotFound, "track not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	key, hash, size, err := s.storage.SaveMedia(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "upload failed: "+err.Error())
		return
	}
	if err := s.store.SetTrackFile(uid, guid, key, hash, size); err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "failed to bind file")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fileKey":     key,
		"contentHash": hash,
		"fileSize":    size,
	})
}

// handleStreamTrack streams a stored media object with range support so the
// client can seek. Requires a valid token (header or ?token= query).
func (s *Server) handleStreamTrack(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	track, err := s.store.GetTrack(uid, r.PathValue("guid"))
	if err != nil || track.FileKey == "" {
		writeErr(w, http.StatusNotFound, "no audio for track")
		return
	}
	f, err := s.storage.OpenObject(track.FileKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidKey) {
			writeErr(w, http.StatusNotFound, "object missing")
			return
		}
		writeErr(w, http.StatusInternalServerError, "open object failed")
		return
	}
	defer f.Close()
	s.serveStored(w, r, f, track.FileKey)
}

// handleTrackArt streams the cover art bound to a track.
func (s *Server) handleTrackArt(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	track, err := s.store.GetTrack(uid, r.PathValue("guid"))
	if err != nil || track.ArtKey == "" {
		writeErr(w, http.StatusNotFound, "no art for track")
		return
	}
	s.serveArt(w, r, track.ArtKey)
}

// handleUploadAsset stores a standalone cover/lyrics blob and returns its key.
func (s *Server) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userID(w, r); !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	key, hash, size, err := s.storage.SaveArt(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "upload failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":    key,
		"sha256": hash,
		"size":   size,
		"url":    "/api/v1/assets/" + key,
	})
}

// handleGetAsset streams a stored art/asset blob.
func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userID(w, r); !ok {
		return
	}
	s.serveArt(w, r, r.PathValue("key"))
}

func (s *Server) serveArt(w http.ResponseWriter, r *http.Request, key string) {
	f, err := s.storage.OpenArt(key)
	if err != nil {
		// An invalid key is reported as "missing" on purpose: telling a caller
		// that a path was rejected would leak the validation rules.
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidKey) {
			writeErr(w, http.StatusNotFound, "asset missing")
			return
		}
		writeErr(w, http.StatusInternalServerError, "open asset failed")
		return
	}
	defer f.Close()
	s.serveStored(w, r, f, key)
}

// serveStored writes a stored object with an accurate Content-Type, a strong
// ETag derived from its content address, and long-lived immutable caching.
//
// Content-addressed keys carry no file extension, so the type has to be
// sniffed from the bytes; relying on the key produced
// application/octet-stream for every cover image, which browsers refuse to
// render inline.
func (s *Server) serveStored(w http.ResponseWriter, r *http.Request, f *os.File, key string) {
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stat object failed")
		return
	}
	h := w.Header()
	h.Set("Content-Type", detectContentType(f))
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", `"`+key+`"`)
	// Content is immutable: the key is the SHA-256 of the bytes.
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	// ServeContent handles Range, If-Range, If-None-Match and HEAD for us.
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// detectContentType sniffs the leading bytes of f and rewinds it so the caller
// can stream the whole file.
func detectContentType(f *os.File) string {
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		return "application/octet-stream"
	}
	if err != nil && err != io.EOF {
		return "application/octet-stream"
	}
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

// --- resumable chunked uploads ---------------------------------------------

type uploadInitRequest struct {
	// TotalChunks is the number of chunks the client will send.
	TotalChunks int `json:"totalChunks"`
	// Target is "media" (audio) or "art" (cover/lyrics). Defaults to media.
	Target string `json:"target"`
}

func (s *Server) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userID(w, r); !ok {
		return
	}
	uploadID := r.PathValue("uploadId")
	var req uploadInitRequest
	if err := readJSON(r, &req); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TotalChunks < 0 || req.TotalChunks > maxChunkCount {
		writeErr(w, http.StatusBadRequest, "invalid totalChunks")
		return
	}
	if req.Target == "" {
		req.Target = "media"
	}
	if req.Target != "media" && req.Target != "art" {
		writeErr(w, http.StatusBadRequest, "invalid target")
		return
	}
	received, _ := s.storage.ReceivedChunks(uploadID)
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadId":    uploadID,
		"chunkSize":   s.cfg.ChunkSize,
		"totalChunks": req.TotalChunks,
		"target":      req.Target,
		"received":    received,
		"maxBytes":    s.cfg.MaxUploadBytes,
	})
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userID(w, r); !ok {
		return
	}
	uploadID := r.PathValue("uploadId")
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index > maxChunkCount {
		writeErr(w, http.StatusBadRequest, "invalid chunk index")
		return
	}
	// Each chunk is at most the advertised chunk size plus a small margin.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.ChunkSize+(1<<20))
	n, err := s.storage.WriteChunk(uploadID, index, r.Body, s.cfg.MaxUploadBytes)
	if err != nil {
		if errors.Is(err, storage.ErrTooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds maximum size")
			return
		}
		writeErr(w, http.StatusBadRequest, "chunk write failed: "+err.Error())
		return
	}
	received, _ := s.storage.ReceivedChunks(uploadID)
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadId": uploadID,
		"index":    index,
		"bytes":    n,
		"received": received,
	})
}

func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userID(w, r); !ok {
		return
	}
	uploadID := r.PathValue("uploadId")
	received, err := s.storage.ReceivedChunks(uploadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "status failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadId":  uploadID,
		"received":  received,
		"chunkSize": s.cfg.ChunkSize,
	})
}

type finalizeRequest struct {
	TotalChunks int    `json:"totalChunks"`
	Target      string `json:"target"`
	// GUID optionally binds the finished object to a track.
	GUID string `json:"guid"`
}

func (s *Server) handleUploadFinalize(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	uploadID := r.PathValue("uploadId")
	var req finalizeRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TotalChunks <= 0 || req.TotalChunks > maxChunkCount {
		writeErr(w, http.StatusBadRequest, "totalChunks required")
		return
	}
	dest := s.storage.MediaDir()
	if req.Target == "art" {
		dest = s.storage.ArtDir()
	}

	key, hash, size, err := s.storage.Finalize(uploadID, req.TotalChunks, dest)
	if err != nil {
		if errors.Is(err, storage.ErrIncomplete) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "finalize failed: "+err.Error())
		return
	}

	result := map[string]any{
		"uploadId":    uploadID,
		"fileKey":     key,
		"contentHash": hash,
		"fileSize":    size,
	}

	// Bind to a track when requested (media target only).
	if req.GUID != "" && req.Target != "art" {
		if err := s.store.SetTrackFile(uid, req.GUID, key, hash, size); err != nil {
			if isNotFound(err) {
				// Object is safely stored but track row is missing; surface the
				// error yet keep the key so the client can retry binding.
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error":   "track not found for binding",
					"fileKey": key,
				})
				return
			}
			writeErr(w, http.StatusInternalServerError, "bind failed")
			return
		}
		result["boundTo"] = req.GUID
	}
	writeJSON(w, http.StatusOK, result)
}
