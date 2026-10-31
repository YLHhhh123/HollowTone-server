package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PlaylistEntry references a track by GUID with an explicit ordering position.
type PlaylistEntry struct {
	TrackGUID string `json:"trackGuid"`
	Position  int    `json:"position"`
}

// Playlist is a synced playlist row.
type Playlist struct {
	GUID      string          `json:"guid"`
	Name      string          `json:"name"`
	Entries   []PlaylistEntry `json:"entries"`
	CreatedAt int64           `json:"createdAt"`
	UpdatedAt int64           `json:"updatedAt"`
	Version   int64           `json:"version"`
	Deleted   bool            `json:"deleted"`
}

// PlaylistInput is the writable subset accepted on upsert.
type PlaylistInput struct {
	GUID        string          `json:"guid"`
	Name        string          `json:"name"`
	Entries     []PlaylistEntry `json:"entries"`
	Deleted     bool            `json:"deleted"`
	BaseVersion int64           `json:"baseVersion"`
}

const playlistColumns = `guid, name, entries_json, created_at, updated_at, version, deleted`

func scanPlaylist(sc interface {
	Scan(dest ...any) error
}) (*Playlist, error) {
	var p Playlist
	var entriesJSON string
	var deleted int
	if err := sc.Scan(&p.GUID, &p.Name, &entriesJSON, &p.CreatedAt, &p.UpdatedAt,
		&p.Version, &deleted); err != nil {
		return nil, err
	}
	if entriesJSON != "" {
		_ = json.Unmarshal([]byte(entriesJSON), &p.Entries)
	}
	if p.Entries == nil {
		p.Entries = []PlaylistEntry{}
	}
	p.Deleted = deleted != 0
	return &p, nil
}

// UpsertPlaylist applies last-write-wins semantics for one playlist, mirroring
// UpsertTrack's conflict behavior.
func (s *Store) UpsertPlaylist(userID int64, in PlaylistInput) (*Playlist, error) {
	if strings.TrimSpace(in.GUID) == "" {
		return nil, fmt.Errorf("playlist guid required")
	}
	now := Now()
	entries := in.Entries
	if entries == nil {
		entries = []PlaylistEntry{}
	}
	entriesJSON := jsonString(entries)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var curVersion int64
	scanErr := tx.QueryRow(
		`SELECT version FROM playlists WHERE guid=? AND user_id=?`, in.GUID, userID,
	).Scan(&curVersion)

	if errors.Is(scanErr, sql.ErrNoRows) {
		_, err = tx.Exec(
			`INSERT INTO playlists(guid, user_id, name, entries_json, created_at, updated_at, version, deleted)
			 VALUES(?,?,?,?,?,?,1,?)`,
			in.GUID, userID, in.Name, entriesJSON, now, now, boolInt(in.Deleted))
		if err != nil {
			return nil, fmt.Errorf("insert playlist: %w", err)
		}
	} else if scanErr != nil {
		return nil, scanErr
	} else {
		if in.BaseVersion != 0 && curVersion > in.BaseVersion {
			cur, err := getPlaylistTx(tx, userID, in.GUID)
			if err != nil {
				return nil, err
			}
			return nil, &ErrConflict{Current: cur}
		}
		_, err = tx.Exec(
			`UPDATE playlists SET name=?, entries_json=?, updated_at=?, version=version+1, deleted=?
			 WHERE guid=? AND user_id=?`,
			in.Name, entriesJSON, now, boolInt(in.Deleted), in.GUID, userID)
		if err != nil {
			return nil, fmt.Errorf("update playlist: %w", err)
		}
	}

	out, err := getPlaylistTx(tx, userID, in.GUID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func getPlaylistTx(tx *sql.Tx, userID int64, guid string) (*Playlist, error) {
	row := tx.QueryRow(
		`SELECT `+playlistColumns+` FROM playlists WHERE guid=? AND user_id=?`, guid, userID)
	p, err := scanPlaylist(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// GetPlaylist loads one playlist by guid.
func (s *Store) GetPlaylist(userID int64, guid string) (*Playlist, error) {
	row := s.db.QueryRow(
		`SELECT `+playlistColumns+` FROM playlists WHERE guid=? AND user_id=?`, guid, userID)
	p, err := scanPlaylist(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ListPlaylistsSince returns playlists updated at or after sinceTS.
func (s *Store) ListPlaylistsSince(userID int64, sinceTS int64) ([]Playlist, error) {
	rows, err := s.db.Query(
		`SELECT `+playlistColumns+` FROM playlists
		 WHERE user_id=? AND updated_at >= ? ORDER BY updated_at ASC`, userID, sinceTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Playlist
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
