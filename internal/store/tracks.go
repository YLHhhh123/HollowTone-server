package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Track is a music-library row as returned to clients.
type Track struct {
	GUID           string  `json:"guid"`
	Title          string  `json:"title"`
	Artist         *string `json:"artist"`
	Album          *string `json:"album"`
	DurationMS     *int64  `json:"durationMs"`
	FileKey        string  `json:"fileKey"`
	FileSize       int64   `json:"fileSize"`
	ContentHash    string  `json:"contentHash"`
	ArtKey         string  `json:"artKey"`
	Lyrics         *string `json:"lyrics"`
	LyricsOffsetMS int64   `json:"lyricsOffsetMs"`
	AddedAt        int64   `json:"addedAt"`
	UpdatedAt      int64   `json:"updatedAt"`
	Version        int64   `json:"version"`
	Deleted        bool    `json:"deleted"`
}

// TrackInput is the writable subset accepted on upsert.
type TrackInput struct {
	GUID           string  `json:"guid"`
	Title          string  `json:"title"`
	Artist         *string `json:"artist"`
	Album          *string `json:"album"`
	DurationMS     *int64  `json:"durationMs"`
	FileKey        string  `json:"fileKey"`
	FileSize       int64   `json:"fileSize"`
	ContentHash    string  `json:"contentHash"`
	ArtKey         string  `json:"artKey"`
	Lyrics         *string `json:"lyrics"`
	LyricsOffsetMS int64   `json:"lyricsOffsetMs"`
	Deleted        bool    `json:"deleted"`
	// BaseVersion is the version the client last saw. Zero means "new entity".
	BaseVersion int64 `json:"baseVersion"`
}

// trackColumns is the shared SELECT list for decoding a Track.
const trackColumns = `guid, title, artist, album, duration_ms, file_key, file_size,
	content_hash, art_key, lyrics, lyrics_offset_ms, added_at, updated_at, version, deleted`

func scanTrack(sc interface {
	Scan(dest ...any) error
}) (*Track, error) {
	var t Track
	var artist, album, fileKey, contentHash, artKey, lyrics sql.NullString
	var dur sql.NullInt64
	var deleted int
	err := sc.Scan(&t.GUID, &t.Title, &artist, &album, &dur, &fileKey, &t.FileSize,
		&contentHash, &artKey, &lyrics, &t.LyricsOffsetMS, &t.AddedAt, &t.UpdatedAt,
		&t.Version, &deleted)
	if err != nil {
		return nil, err
	}
	t.Artist = nullStr(artist)
	t.Album = nullStr(album)
	t.FileKey = fileKey.String
	t.ContentHash = contentHash.String
	t.ArtKey = artKey.String
	t.Lyrics = nullStr(lyrics)
	if dur.Valid {
		v := dur.Int64
		t.DurationMS = &v
	}
	t.Deleted = deleted != 0
	return &t, nil
}

func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// UpsertTrack applies last-write-wins semantics for one track.
//
//   - If the track does not exist it is inserted with version 1.
//   - If it exists and input.BaseVersion is 0 (client forces write) or matches
//     the stored version, the row is updated and version is incremented.
//   - If it exists and the stored version is newer than BaseVersion, an
//     *ErrConflict carrying the current row is returned.
func (s *Store) UpsertTrack(userID int64, in TrackInput) (*Track, error) {
	if strings.TrimSpace(in.GUID) == "" {
		return nil, fmt.Errorf("track guid required")
	}
	now := Now()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var curVersion int64
	var addedAt int64
	scanErr := tx.QueryRow(
		`SELECT version, added_at FROM tracks WHERE guid = ? AND user_id = ?`,
		in.GUID, userID,
	).Scan(&curVersion, &addedAt)

	if errors.Is(scanErr, sql.ErrNoRows) {
		_, err = tx.Exec(
			`INSERT INTO tracks(guid, user_id, title, artist, album, duration_ms, file_key,
				file_size, content_hash, art_key, lyrics, lyrics_offset_ms, added_at,
				updated_at, version, deleted)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?)`,
			in.GUID, userID, in.Title, in.Artist, in.Album, in.DurationMS, in.FileKey,
			in.FileSize, in.ContentHash, in.ArtKey, in.Lyrics, in.LyricsOffsetMS, now,
			now, boolInt(in.Deleted),
		)
		if err != nil {
			return nil, fmt.Errorf("insert track: %w", err)
		}
	} else if scanErr != nil {
		return nil, scanErr
	} else {
		if in.BaseVersion != 0 && curVersion > in.BaseVersion {
			cur, err := getTrackTx(tx, userID, in.GUID)
			if err != nil {
				return nil, err
			}
			return nil, &ErrConflict{Current: cur}
		}
		_, err = tx.Exec(
			`UPDATE tracks SET title=?, artist=?, album=?, duration_ms=?, file_key=?,
				file_size=?, content_hash=?, art_key=?, lyrics=?, lyrics_offset_ms=?,
				updated_at=?, version=version+1, deleted=?
			 WHERE guid=? AND user_id=?`,
			in.Title, in.Artist, in.Album, in.DurationMS, in.FileKey, in.FileSize,
			in.ContentHash, in.ArtKey, in.Lyrics, in.LyricsOffsetMS, now,
			boolInt(in.Deleted), in.GUID, userID,
		)
		if err != nil {
			return nil, fmt.Errorf("update track: %w", err)
		}
	}

	out, err := getTrackTx(tx, userID, in.GUID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func getTrackTx(tx *sql.Tx, userID int64, guid string) (*Track, error) {
	row := tx.QueryRow(
		`SELECT `+trackColumns+` FROM tracks WHERE guid=? AND user_id=?`, guid, userID)
	t, err := scanTrack(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// GetTrack loads one track by guid.
func (s *Store) GetTrack(userID int64, guid string) (*Track, error) {
	row := s.db.QueryRow(
		`SELECT `+trackColumns+` FROM tracks WHERE guid=? AND user_id=?`, guid, userID)
	t, err := scanTrack(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ListTracksSince returns every track changed at or after sinceTS (updated_at),
// including tombstones so clients can mirror deletions. When sinceTS is 0 the
// full library is returned.
func (s *Store) ListTracksSince(userID int64, sinceTS int64) ([]Track, error) {
	rows, err := s.db.Query(
		`SELECT `+trackColumns+` FROM tracks
		 WHERE user_id=? AND updated_at >= ?
		 ORDER BY updated_at ASC`, userID, sinceTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// SetTrackFile attaches an uploaded file to a track after storage completes.
// It does not bump the logical version beyond what the metadata write did,
// but updates file fields and updated_at so sync picks up the change.
func (s *Store) SetTrackFile(userID int64, guid, fileKey, contentHash string, size int64) error {
	res, err := s.db.Exec(
		`UPDATE tracks SET file_key=?, content_hash=?, file_size=?, updated_at=?, version=version+1
		 WHERE guid=? AND user_id=?`,
		fileKey, contentHash, size, Now(), guid, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TrackByContentHash finds a live track by content hash (used for dedup).
func (s *Store) TrackByContentHash(userID int64, hash string) (*Track, error) {
	row := s.db.QueryRow(
		`SELECT `+trackColumns+` FROM tracks
		 WHERE user_id=? AND content_hash=? AND deleted=0 LIMIT 1`, userID, hash)
	t, err := scanTrack(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// jsonString is a small helper for optional JSON columns.
func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
