// Package storage implements a content-addressed object store for uploaded
// audio files and cover/lyrics sidecars, plus resumable chunked upload
// staging. Objects are stored on the local filesystem under a two-level
// hash fan-out so directories stay small even with large libraries.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrIncomplete is returned by Finalize when not all chunks are present.
var ErrIncomplete = errors.New("upload incomplete")

// Store manages the object store and upload staging directories.
type Store struct {
	mediaDir   string
	artDir     string
	uploadDir  string
	tmpDir     string
	mu         sync.Mutex
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

func fanout(root, key string) string {
	if len(key) < 4 {
		return filepath.Join(root, key)
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
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return "", "", 0, fmt.Errorf("write temp: %w", err)
	}
	hash = hex.EncodeToString(h.Sum(nil))
	key = KeyForHash(hash)
	dst := fanout(root, key)

	if _, statErr := os.Stat(dst); statErr == nil {
		return key, hash, size, nil // dedup hit
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", "", 0, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		// Rename can fail across filesystems; fall back to copy.
		if cpErr := copyFile(tmp.Name(), dst); cpErr != nil {
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
	return out
}

// WriteChunk stores one chunk of a resumable upload. Chunks may arrive in any
// order; index is the zero-based chunk number.
func (s *Store) WriteChunk(uploadID string, index int, data io.Reader) (int64, error) {
	if index < 0 {
		return 0, fmt.Errorf("invalid chunk index")
	}
	dir := s.chunkDir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.part", index))
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, data)
	cerr := f.Close()
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if cerr != nil {
		os.Remove(tmp)
		return 0, cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return n, nil
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

// Finalize assembles all chunks into a complete media object. It requires
// chunk indices 0..totalChunks-1 to all be present. Returns the object key,
// hash and size. On success the staging directory is removed.
func (s *Store) Finalize(uploadID string, totalChunks int, dest string) (key, hash string, size int64, err error) {
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
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
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
	size = int64(h.Size())
	hash = hex.EncodeToString(h.Sum(nil))
	key = KeyForHash(hash)
	dstDir := dest // media or art root
	if err := os.MkdirAll(filepath.Dir(fanout(dstDir, key)), 0o755); err != nil {
		return "", "", 0, err
	}
	tmp.Close()
	dst := fanout(dstDir, key)
	if _, statErr := os.Stat(dst); statErr != nil {
		if err := os.Rename(tmp.Name(), dst); err != nil {
			if cpErr := copyFile(tmp.Name(), dst); cpErr != nil {
				return "", "", 0, cpErr
			}
		}
	}
	// Dedup may have hit an existing object; recover its real size.
	if fi, err := os.Stat(dst); err == nil {
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
	f, err := os.Open(s.ObjectPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// OpenArt opens a stored art object for reading.
func (s *Store) OpenArt(key string) (*os.File, error) {
	f, err := os.Open(s.ArtPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// ErrNotFound signals a missing object.
var ErrNotFound = errors.New("object not found")

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
