// Package store owns the SQLite database: schema, migrations, and all
// persistence operations for users, tracks, playlists and sync metadata.
//
// All client-facing entities carry monotonically increasing per-entity
// `version` counters. A write that supplies a `base_version` uses
// last-write-wins: if the stored version is newer than the client's base
// version the server returns a conflict carrying the current row, so the
// client can re-merge.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned by versioned writes when the caller's base version
// is stale. It carries the current server-side row for the caller.
type ErrConflict struct {
	Current any
}

func (e *ErrConflict) Error() string { return "version conflict" }

// Store wraps the SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and runs
// migrations. It uses the pure-Go modernc.org/sqlite driver so the server
// builds and runs without CGO or a C toolchain.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite is a single-writer engine; a small pool keeps WAL happy and
	// avoids "database is locked" under concurrent request load.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for health checks and advanced queries.
func (s *Store) DB() *sql.DB { return s.db }

// schemaVersion is recorded so future migrations can branch on it.
const schemaVersion = 2

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at    INTEGER NOT NULL
		)`,
		// tracks: library metadata, keyed by a stable client-generated GUID so
		// multiple devices can converge on the same logical track.
		`CREATE TABLE IF NOT EXISTS tracks (
			guid         TEXT PRIMARY KEY,
			user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title        TEXT NOT NULL DEFAULT '',
			artist       TEXT,
			album        TEXT,
			duration_ms  INTEGER,
			-- Remote-relative object reference (content-addressed file), or empty
			-- for metadata-only rows that are still being uploaded.
			file_key     TEXT,
			file_size    INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT,
			art_key      TEXT,
			lyrics       TEXT,
			lyrics_offset_ms INTEGER NOT NULL DEFAULT 0,
			added_at     INTEGER NOT NULL,
			updated_at   INTEGER NOT NULL,
			version      INTEGER NOT NULL DEFAULT 1,
			deleted      INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_user_updated ON tracks(user_id, updated_at)`,
		// Deliberately NOT unique. The object store already deduplicates bytes,
		// and two library entries may legitimately reference the same audio
		// file; the previous UNIQUE index made the second bind fail with a 500.
		`DROP INDEX IF EXISTS idx_tracks_content`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_content_hash ON tracks(user_id, content_hash) WHERE content_hash IS NOT NULL AND deleted = 0`,

		`CREATE TABLE IF NOT EXISTS playlists (
			guid       TEXT PRIMARY KEY,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name       TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			version    INTEGER NOT NULL DEFAULT 1,
			deleted    INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_playlists_user_updated ON playlists(user_id, updated_at)`,

		// playlist_entries are replaced wholesale on each playlist write, encoded
		// as ordered JSON on the playlist row instead of a child table. Keeping
		// ordering simple avoids position-conflict resolution entirely.
		// (See playlists.entries_json added below.)
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	// Add entries_json column to playlists when upgrading from an early schema.
	if err := s.addColumnIfMissing("playlists", "entries_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}

	var current int
	_ = s.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='schema_version'`).Scan(&current)
	if current < schemaVersion {
		if _, err := s.db.Exec(
			`INSERT INTO meta(key, value) VALUES('schema_version', ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			fmt.Sprint(schemaVersion)); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
	}
	return nil
}

func (s *Store) addColumnIfMissing(table, column, def string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return fmt.Errorf("table_info %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, def))
	return err
}

// Now returns the current Unix-millisecond timestamp used across all rows.
func Now() int64 { return time.Now().UnixMilli() }
