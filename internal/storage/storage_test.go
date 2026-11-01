package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validHash is a well-formed content address: 64 lowercase hex characters.
const validHash = "1c6ce95b0e82c1d4b6c8b0a1e2f3a4b5c6d7e8f90123456789abcdef01234567"

func TestValidKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"bare hash", validHash, true},
		{"hash with extension", validHash + ".jpg", true},
		{"empty", "", false},
		{"too short", "abc123", false},
		{"uppercase hex", strings.ToUpper(validHash), false},
		{"parent traversal", "../../../../etc/passwd", false},
		{"dotdot prefix", "..", false},
		{"absolute path", "/etc/passwd", false},
		{"nested traversal", validHash + "/../../.jwt_secret", false},
		{"backslash traversal", `..\..\.jwt_secret`, false},
		{"non-hex character", strings.Replace(validHash, "a", "z", 1), false},
		{"overlong extension", validHash + ".abcdefghij", false},
		{"empty extension", validHash + ".", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidKey(tc.key); got != tc.want {
				t.Fatalf("ValidKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestFanoutCannotEscapeRoot is the regression guard for the path-traversal
// class of bug: a crafted key must never resolve outside the store root.
func TestFanoutCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{
		"../../../../etc/passwd",
		"..",
		"../.jwt_secret",
		`..\..\windows\win.ini`,
		"",
		"short",
	} {
		if got := fanout(root, key); got != "" {
			t.Fatalf("fanout(%q) = %q, want empty (fail closed)", key, got)
		}
	}
	if got := fanout(root, validHash); got == "" {
		t.Fatal("fanout rejected a valid key")
	}
}

func TestOpenRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s, err := New(filepath.Join(root, "media"), filepath.Join(root, "art"), filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Plant a secret one level above the art store.
	secret := filepath.Join(root, ".jwt_secret")
	if err := os.WriteFile(secret, []byte("super-secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	for _, key := range []string{"../.jwt_secret", "../../.jwt_secret", "..%2f.jwt_secret"} {
		if _, err := s.OpenArt(key); err == nil {
			t.Fatalf("OpenArt(%q) succeeded, expected rejection", key)
		}
	}
}

func TestWriteChunkEnforcesTotalBudget(t *testing.T) {
	root := t.TempDir()
	s, err := New(filepath.Join(root, "media"), filepath.Join(root, "art"), filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const budget = 10
	if _, err := s.WriteChunk("up-1", 0, strings.NewReader("12345"), budget); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if _, err := s.WriteChunk("up-1", 1, strings.NewReader("12345"), budget); err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	// Budget is now exhausted; the next chunk must be refused.
	if _, err := s.WriteChunk("up-1", 2, strings.NewReader("1"), budget); err == nil {
		t.Fatal("expected ErrTooLarge once the budget is exhausted")
	}
	// Overwriting an existing chunk stays within budget and must succeed.
	if _, err := s.WriteChunk("up-1", 0, strings.NewReader("12345"), budget); err != nil {
		t.Fatalf("rewriting chunk 0 should be allowed: %v", err)
	}
}

func TestCleanupStaleUploads(t *testing.T) {
	root := t.TempDir()
	s, err := New(filepath.Join(root, "media"), filepath.Join(root, "art"), filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.WriteChunk("stale", 0, strings.NewReader("abc"), 0); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	dir := s.chunkDir("stale")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	removed, err := s.CleanupStaleUploads(24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupStaleUploads: %v", err)
	}
	if removed == 0 {
		t.Fatal("expected the stale upload directory to be removed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("stale dir still present: %v", err)
	}
}
