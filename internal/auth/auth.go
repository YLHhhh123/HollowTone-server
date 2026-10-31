// Package auth issues and verifies JWT access tokens and provides the HTTP
// middleware that gates the API behind a valid token.
package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey int

const userIDKey ctxKey = iota

// Claims is the JWT payload.
type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"usr"`
	jwt.RegisteredClaims
}

// Manager mints and validates tokens.
type Manager struct {
	secret []byte
	ttl    time.Duration
}

// New creates a token manager.
func New(secret []byte, ttl time.Duration) *Manager {
	return &Manager{secret: secret, ttl: ttl}
}

// Issue returns a signed token for the given user.
func (m *Manager) Issue(userID int64, username string) (string, time.Time, error) {
	now := time.Now()
	expiry := now.Add(m.ttl)
	claims := Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiry),
			Subject:   username,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	return signed, expiry, err
}

// Parse validates a token string and returns its claims.
func (m *Manager) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// Middleware rejects requests without a valid bearer token and injects the
// authenticated user id into the request context.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		tokenStr := ""
		if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
			tokenStr = strings.TrimSpace(authz[7:])
		} else if q := r.URL.Query().Get("token"); q != "" {
			// Allow token in query string for media streaming / <audio> tags
			// that cannot set headers.
			tokenStr = q
		}
		if tokenStr == "" {
			unauthorized(w, "missing token")
			return
		}
		claims, err := m.Parse(tokenStr)
		if err != nil {
			unauthorized(w, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserID extracts the authenticated user id from the request context.
func UserID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"` + msg + `"}`))
}
