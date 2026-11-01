// Package storage implements a content-addressed object store for uploaded
// audio files and cover/lyrics sidecars, plus resumable chunked upload
// staging. Objects are stored on the local filesystem under a two-level
// hash fan-out so directories stay small even with large libraries.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrIncomplete is returned by Finalize when not all chunks are present.
var ErrIncomplete = errors.New("upload incomplete")

// ErrNotFound signals a missing object.
var ErrNotFound = errors.New("object not found")

// ErrInvalidKey signals a key that is not a well-formed content address.
var ErrInvalidKey = errors.New("invalid object key")

// ErrTooLarge is returned when a resumable upload exceeds its total budget.
var ErrTooLarge = errors.New("upload exceeds maximum size")

// maxChunkIndex bounds the chunk ordinal accepted for a single upload session.
const maxChunkIndex = 100_000

// Store manages the object store and upload staging directories.
type Store struct {
	mediaDir  string
	artDir    string
	uploadDir string
	tmpDir    string
}

// New creates a Store rooted at the given directories.
func New(mediaDir, artDir, uploadDir string) (*Store, error) {
	tmpDir := filepath.Join(filepath.Dir(uploadDir), "tmp")
	for _, d := range []string{mediaDir, artDir, uploadDir, tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return &Store{mediaDir: mediaDir, artDir: artDir, uploadDir: uploadDir, tmpDir: tmpDir}, nil
}

// ValidKey reports whether key is a safe content-addressed object key.
//
// Keys are SHA-256 hex digests, optionally suffixed with a short lowercase
// extension. Validating shape rather than merely cleaning a path is what stops
// a crafted request such as /api/v1/assets/..%2f..%2f.jwt_secret from escaping
// the object-store root and reading arbitrary files.
func ValidKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	base := key
	if i := strings.IndexByte(key, '.'); i >= 0 {
		ext := key[i+1:]
		base = key[:i]
		if len(ext) == 0 || len(ext) > 8 {
			return false
		}
		for _, r := range ext {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
				return false
			}
		}
	}
	if len(base) != 64 {
		return false
	}
	for _, r := range base {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// fanout maps a validated key onto its two-level directory. An invalid key
// yields an empty string so callers fail closed.
func fanout(root, key string) string {
	if !ValidKey(key) {
		return ""
	}
	return filepath.Join(root, key[0:2], key[2:4], key)
}

// ObjectPath returns the on-disk path for a media content key.
func (s *Store) ObjectPath(key string) string { return fanout(s.mediaDir, key) }

// ArtPath returns the on-disk path for a cover/lyrics content key.
func (s *Store) ArtPath(key string) string { return fanout(s.artDir, key) }

// KeyForHash derives a stable object key from a SHA-256 hex digest.
func KeyForHash(hash string) string { return hash }

// HashReader streams r and returns its SHA-256 hex digest and byte count.
func HashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// --- Whole-file save (small files / simple path) ---------------------------

// SaveMedia streams src into the media store, computing its hash, and returns
// the object key, hash and size. If an object with the same hash already
// exists it is reused (deduplicated).
func (s *Store) SaveMedia(r io.Reader) (key, hash string, size int64, err error) {
	return s.saveInto(s.mediaDir, r, "media")
}

// SaveArt streams src into the art store alongside media.
func (s *Store) SaveArt(r io.Reader) (key, hash string, size int64, err error) {
	return s.saveInto(s.artDir, r, "art")
}

func (s *Store) saveInto(root string, r io.Reader, kind string) (key, hash string, size int64, err error) {
	tmp, err := os.CreateTemp(s.tmpDir, kind+"-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	closed := false
	closeTmp := func() error {
		if closed {
			return nil
		}
		closed = true
		return tmp.Close()
	}
	defer func() {
		closeTmp()
		os.Remove(tmpName)
	}()

	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return "", "", 0, fmt.Errorf("write temp: %w", err)
	}
	hash = hex.EncodeToString(h.Sum(nil))
	key = KeyForHash(hash)
	dst := fanout(root, key)
	if dst == "" {
		return "", "", 0, ErrInvalidKey
	}

	if _, statErr := os.Stat(dst); statErr == nil {
		return key, hash, size, nil // dedup hit: existing bytes are identical
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", 0, err
	}
	if err := closeTmp(); err != nil {
		return "", "", 0, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		// Rename can fail across filesystems; fall back to copy.
		if cpErr := copyFile(tmpName, dst); cpErr != nil {
			return "", "", 0, cpErr
		}
	}
	return key, hash, size, nil
}

// --- Chunked resumable uploads ---------------------------------------------

// chunkDir is the staging directory for one upload session.
func (s *Store) chunkDir(uploadID string) string {
	return filepath.Join(s.uploadDir, sanitize(uploadID))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "upload"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// chunkPath returns the path of one staged chunk.
func (s *Store) chunkPath(uploadID string, index int) string {
	return filepath.Join(s.chunkDir(uploadID), fmt.Sprintf("%d.part", index))
}

// WriteChunk stores one chunk of a resumable upload. Chunks may arrive in any
// order; index is the zero-based chunk number. maxTotal caps the cumulative
// bytes staged for a session so a client cannot exhaust the disk by streaming
// endless chunks under one upload id.
func (s *Store) WriteChunk(uploadID string, index int, data io.Reader, maxTotal int64) (int64, error) {
	if index < 0 || index > maxChunkIndex {
		return 0, fmt.Errorf("invalid chunk index")
	}
	dir := s.chunkDir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	if maxTotal <= 0 {
		return s.writeChunkFile(uploadID, index, data)
	}

	used, err := dirBytes(dir)
	if err != nil {
		return 0, err
	}
	// Re-writing a chunk reuses its own bytes, so exclude them first.
	if st, statErr := os.Stat(s.chunkPath(uploadID, index)); statErr == nil {
		used -= st.Size()
	}
	if used >= maxTotal {
		return 0, ErrTooLarge
	}
	remaining := maxTotal - used

	// Read one byte past the budget so overshoot is detectable.
	n, err := s.writeChunkFile(uploadID, index, io.LimitReader(data, remaining+1))
	if err != nil {
		return n, err
	}
	if n > remaining {
		os.Remove(s.chunkPath(uploadID, index))
		return 0, ErrTooLarge
	}
	return n, nil
}

func (s *Store) writeChunkFile(uploadID string, index int, data io.Reader) (int64, error) {
	path := s.chunkPath(uploadID, index)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, data)
	cerr := f.Close()
	if err != nil {
		os.Remove(tmp)
		return n, err
	}
	if cerr != nil {
		os.Remove(tmp)
		return n, cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return n, err
	}
	return n, nil
}

// dirBytes sums the size of the regular files directly inside dir.
func dirBytes(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			total += fi.Size()
		}
	}
	return total, nil
}

// ReceivedChunks returns the sorted list of chunk indices already uploaded.
func (s *Store) ReceivedChunks(uploadID string) ([]int, error) {
	dir := s.chunkDir(uploadID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []int{}, nil
		}
		return nil, err
	}
	var idx []int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".part"))
		if err == nil {
			idx = append(idx, n)
		}
	}
	sort.Ints(idx)
	return idx, nil
}

// Finalize assembles all chunks into a complete object. It requires chunk
// indices 0..totalChunks-1 to all be present. Returns the object key, hash and
// size. On success the staging directory is removed.
func (s *Store) Finalize(uploadID string, totalChunks int, dest string) (key, hash string, size int64, err error) {
	if totalChunks <= 0 || totalChunks > maxChunkIndex {
		return "", "", 0, fmt.Errorf("invalid chunk count")
	}
	dir := s.chunkDir(uploadID)
	received, err := s.ReceivedChunks(uploadID)
	if err != nil {
		return "", "", 0, err
	}
	have := make(map[int]bool, len(received))
	for _, i := range received {
		have[i] = true
	}
	for i := 0; i < totalChunks; i++ {
		if !have[i] {
			return "", "", 0, fmt.Errorf("%w: missing chunk %d", ErrIncomplete, i)
		}
	}

	tmp, err := os.CreateTemp(s.tmpDir, "finalize-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	closed := false
	closeTmp := func() error {
		if closed {
			return nil
		}
		closed = true
		return tmp.Close()
	}
	defer func() {
		closeTmp()
		os.Remove(tmpName)
	}()

	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	for i := 0; i < totalChunks; i++ {
		cf, err := os.Open(filepath.Join(dir, fmt.Sprintf("%d.part", i)))
		if err != nil {
			return "", "", 0, err
		}
		if _, err := io.Copy(w, cf); err != nil {
			cf.Close()
			return "", "", 0, err
		}
		cf.Close()
	}
	hash = hex.EncodeToString(h.Sum(nil))
	key = KeyForHash(hash)
	dst := fanout(dest, key)
	if dst == "" {
		return "", "", 0, ErrInvalidKey
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", 0, err
	}
	if err := closeTmp(); err != nil {
		return "", "", 0, err
	}
	if _, statErr := os.Stat(dst); statErr != nil {
		if err := os.Rename(tmpName, dst); err != nil {
			if cpErr := copyFile(tmpName, dst); cpErr != nil {
				return "", "", 0, cpErr
			}
		}
	}
	// Dedup may have hit an existing object; report its real size.
	if fi, statErr := os.Stat(dst); statErr == nil {
		size = fi.Size()
	}
	os.RemoveAll(dir)
	return key, hash, size, nil
}

// MediaDir returns the media root, used by Finalize callers.
func (s *Store) MediaDir() string { return s.mediaDir }

// ArtDir returns the art root.
func (s *Store) ArtDir() string { return s.artDir }

// OpenObject opens a stored media object for reading.
func (s *Store) OpenObject(key string) (*os.File, error) {
	return openValidated(s.ObjectPath(key), key)
}

// OpenArt opens a stored art object for reading.
func (s *Store) OpenArt(key string) (*os.File, error) {
	return openValidated(s.ArtPath(key), key)
}

func openValidated(path, key string) (*os.File, error) {
	if !ValidKey(key) || path == "" {
		return nil, ErrInvalidKey
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// --- housekeeping ----------------------------------------------------------

// CleanupStaleUploads removes staging directories and temp files older than
// maxAge. Abandoned resumable uploads would otherwise pin disk space forever,
// because nothing else ever deletes them.
func (s *Store) CleanupStaleUploads(maxAge time.Duration) (removed int, err error) {
	cutoff := time.Now().Add(-maxAge)
	entries, err := os.ReadDir(s.uploadDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.uploadDir, e.Name())); err == nil {
			removed++
		}
	}
	// Leftover temp files from crashed writes.
	if tmps, err := os.ReadDir(s.tmpDir); err == nil {
		for _, e := range tmps {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(s.tmpDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// StartJanitor runs CleanupStaleUploads periodically until ctx is cancelled.
func (s *Store) StartJanitor(ctx context.Context, interval, maxAge time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := s.CleanupStaleUploads(maxAge); err != nil {
					log.Printf("upload janitor: %v", err)
				} else if n > 0 {
					log.Printf("upload janitor: removed %d stale entries", n)
				}
			}
		}
	}()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
