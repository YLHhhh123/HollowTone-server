// Package config loads the HollowTone personal-server configuration from
// environment variables, with sane defaults that work out of the box for a
// single-user NAS / personal machine deployment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// DataDir is the root directory for the SQLite database and object store.
	DataDir string
	// DBPath is the SQLite database file path.
	DBPath string
	// MediaDir stores uploaded audio files (keyed by content hash).
	MediaDir string
	// ArtDir stores cover art / lyrics sidecar files.
	ArtDir string
	// JWTSecret signs access tokens.
	JWTSecret []byte
	// TokenTTL is how long an issued access token stays valid.
	TokenTTL time.Duration
	// MaxUploadBytes caps a single whole-file or chunked upload size.
	MaxUploadBytes int64
	// ChunkSize is the server-advertised chunk size for chunked uploads.
	ChunkSize int64
	// AllowRegister enables the public /auth/register endpoint.
	AllowRegister bool
	// TrustProxy makes the server honour X-Forwarded-For / X-Real-IP when
	// resolving the caller address. Only enable it behind a reverse proxy you
	// control, otherwise clients can spoof their address.
	TrustProxy bool
	// CORSOrigins lists the browser origins allowed to call the API. Empty
	// disables CORS entirely (the mobile client does not need it).
	CORSOrigins []string
	// UploadTTL is how long an unfinished resumable upload is kept before the
	// janitor removes it.
	UploadTTL time.Duration
	// JanitorInterval is how often stale uploads are swept. Zero disables it.
	JanitorInterval time.Duration
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	dataDir := env("HT_DATA_DIR", "./data")
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}

	cfg := &Config{
		Addr:          env("HT_ADDR", ":8080"),
		DataDir:       dataDir,
		DBPath:        env("HT_DB_PATH", filepath.Join(dataDir, "hollowtone_server.sqlite")),
		MediaDir:      env("HT_MEDIA_DIR", filepath.Join(dataDir, "media")),
		ArtDir:        env("HT_ART_DIR", filepath.Join(dataDir, "art")),
		TokenTTL:      envDuration("HT_TOKEN_TTL", 30*24*time.Hour),
		AllowRegister: envBool("HT_ALLOW_REGISTER", true),
	}

	cfg.MaxUploadBytes = envInt64("HT_MAX_UPLOAD_BYTES", 2<<30) // 2 GiB
	cfg.ChunkSize = envInt64("HT_CHUNK_SIZE", 8<<20)            // 8 MiB
	cfg.UploadTTL = envDuration("HT_UPLOAD_TTL", 24*time.Hour)
	cfg.JanitorInterval = envDuration("HT_JANITOR_INTERVAL", 1*time.Hour)

	if err := cfg.ensureDirs(); err != nil {
		return nil, err
	}

	secret := os.Getenv("HT_JWT_SECRET")
	if secret == "" {
		// Generate an ephemeral secret and persist it next to the DB so restarts
		// keep sessions valid without requiring manual configuration.
		generated, err := loadOrCreateSecret(filepath.Join(dataDir, ".jwt_secret"))
		if err != nil {
			return nil, err
		}
		secret = generated
	}
	cfg.JWTSecret = []byte(secret)

	// Legacy switch kept for compatibility with existing deployments.
	cfg.TrustProxy = envBool("HT_TRUSTED_PROXY_ALL", false) || envBool("HT_TRUST_PROXY", false)
	cfg.CORSOrigins = splitList(os.Getenv("HT_CORS_ORIGINS"))

	return cfg, nil
}

// splitList parses a comma-separated environment list, dropping blanks.
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) ensureDirs() error {
	for _, dir := range []string{c.DataDir, c.MediaDir, c.ArtDir, filepath.Dir(c.DBPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}

// ChunkUploadDir returns the temporary staging directory for in-progress
// chunked uploads.
func (c *Config) ChunkUploadDir() string {
	return filepath.Join(c.DataDir, "uploads")
}

func loadOrCreateSecret(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate jwt secret: %w", err)
	}
	secret := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", fmt.Errorf("persist jwt secret: %w", err)
	}
	return secret, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
