package api

import (
	"errors"
	"net/http"

	"github.com/YLHhhh123/HollowTone-server/internal/store"
)

// handleSync returns everything changed since a watermark so clients can mirror
// the server library incrementally. Response includes the server's high-water
// timestamp to pass as `since` on the next call.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	since := parseSince(r)
	serverTime := store.Now()

	tracks, err := s.store.ListTracksSince(uid, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list tracks")
		return
	}
	playlists, err := s.store.ListPlaylistsSince(uid, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list playlists")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"serverTime": serverTime,
		"since":      since,
		"tracks":     tracks,
		"playlists":  playlists,
	})
}

// handleUpsertTrack applies last-write-wins for one track.
func (s *Server) handleUpsertTrack(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	var in store.TrackInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.GUID = guid

	track, err := s.store.UpsertTrack(uid, in)
	if err != nil {
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			writeConflict(w, conflict.Current)
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, track)
}

// handleGetTrack fetches a single track.
func (s *Server) handleGetTrack(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	track, err := s.store.GetTrack(uid, r.PathValue("guid"))
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "failed to get track")
		return
	}
	writeJSON(w, http.StatusOK, track)
}

// handleUpsertPlaylist applies last-write-wins for one playlist.
func (s *Server) handleUpsertPlaylist(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	var in store.PlaylistInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.GUID = guid

	pl, err := s.store.UpsertPlaylist(uid, in)
	if err != nil {
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			writeConflict(w, conflict.Current)
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pl)
}

// handleGetPlaylist fetches a single playlist.
func (s *Server) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	pl, err := s.store.GetPlaylist(uid, r.PathValue("guid"))
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "playlist not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "failed to get playlist")
		return
	}
	writeJSON(w, http.StatusOK, pl)
}
