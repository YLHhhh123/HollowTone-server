package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// User is a server account.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"createdAt"`
}

// ErrUserExists is returned when the username is already taken.
var ErrUserExists = errors.New("username already exists")

// CreateUser hashes the password and inserts a new user.
func (s *Store) CreateUser(username, password string) (*User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 2 {
		return nil, fmt.Errorf("username too short")
	}
	if len(password) < 6 {
		return nil, fmt.Errorf("password too short")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	now := Now()
	res, err := s.db.Exec(
		`INSERT INTO users(username, password_hash, created_at) VALUES(?, ?, ?)`,
		username, string(hash), now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, CreatedAt: now}, nil
}

// Authenticate verifies a username/password pair, returning the user on success.
func (s *Store) Authenticate(username, password string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`,
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrNotFound
	}
	return &u, nil
}

// UserByID loads a user by id.
func (s *Store) UserByID(id int64) (*User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CountUsers reports the number of registered accounts, used to decide whether
// the first account may be bootstrapped without an invite.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}
