package api

import (
	"errors"
	"net/http"

	"github.com/YLHhhh123/HollowTone-server/internal/store"
)

type authRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
	User      struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// handleRegister creates an account. Registration is allowed when the server
// is configured to permit it, or for the very first user (bootstrap).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	limitKey, ok := s.throttleApply(w, r, "register")
	if !ok {
		return
	}
	var req authRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !s.cfg.AllowRegister {
		count, _ := s.store.CountUsers()
		if count > 0 {
			writeErr(w, http.StatusForbidden, "registration is disabled")
			return
		}
	}
	user, err := s.store.CreateUser(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, store.ErrUserExists) {
			writeErr(w, http.StatusConflict, "username already exists")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.authLimiter.reset(limitKey)
	s.issue(w, user)
}

// handleLogin authenticates and returns a token.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	limitKey, ok := s.throttleApply(w, r, "login")
	if !ok {
		return
	}
	var req authRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	user, err := s.store.Authenticate(req.Username, req.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	// Successful sign-in clears the budget so typos do not lock the owner out.
	s.authLimiter.reset(limitKey)
	s.issue(w, user)
}

func (s *Server) issue(w http.ResponseWriter, user *store.User) {
	token, expiry, err := s.tokens.Issue(user.ID, user.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	var resp authResponse
	resp.Token = token
	resp.ExpiresAt = expiry.UnixMilli()
	resp.User.ID = user.ID
	resp.User.Username = user.Username
	writeJSON(w, http.StatusOK, resp)
}

// handleMe returns the current user identity.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.userID(w, r)
	if !ok {
		return
	}
	user, err := s.store.UserByID(uid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        user.ID,
		"username":  user.Username,
		"createdAt": user.CreatedAt,
	})
}
