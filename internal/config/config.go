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
	// TrustedProxies is reserved for future reverse-proxy setups.
	TrustedProxies []string
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	dataDir := env("HT_DATA_DIR", "./data")
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}

	cfg := &Config{
		Addr:        env("HT_ADDR", ":8080"),
		DataDir:     dataDir,
		DBPath:      env("HT_DB_PATH", filepath.Join(dataDir, "hollowtone_server.sqlite")),
		MediaDir:    env("HT_MEDIA_DIR", filepath.Join(dataDir, "media")),
		ArtDir:      env("HT_ART_DIR", filepath.Join(dataDir, "art")),
		TokenTTL:    envDuration("HT_TOKEN_TTL", 30*24*time.Hour),
		AllowRegister: envBool("HT_ALLOW_REGISTER", true),
	}

	cfg.MaxUploadBytes = envInt64("HT_MAX_UPLOAD_BYTES", 2<<30) // 2 GiB
	cfg.ChunkSize = envInt64("HT_CHUNK_SIZE", 8<<20)           // 8 MiB

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

	if envBool("HT_TRUSTED_PROXY_ALL", false) {
		cfg.TrustedProxies = []string{"0.0.0.0/0"}
	}

	return cfg, nil
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
