package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultChunkSize     = int64(8 * 1024 * 1024)
	defaultMaxUploadSize = int64(100 * 1024 * 1024 * 1024)
)

type Config struct {
	ListenAddr      string
	StorageRoot     string
	HostPathPrefix  string
	Password        string
	CookieSecure    bool
	MaxUploadSize   int64
	UploadChunkSize int64
}

func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr:      envOrDefault("LISTEN_ADDR", ":8080"),
		StorageRoot:     envOrDefault("STORAGE_ROOT", "/data"),
		HostPathPrefix:  strings.TrimSpace(os.Getenv("HOST_PATH_PREFIX")),
		Password:        os.Getenv("APP_PASSWORD"),
		CookieSecure:    envBool("COOKIE_SECURE", false),
		MaxUploadSize:   envInt64("MAX_UPLOAD_SIZE", defaultMaxUploadSize),
		UploadChunkSize: envInt64("UPLOAD_CHUNK_SIZE", defaultChunkSize),
	}

	root, err := filepath.Abs(cfg.StorageRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve STORAGE_ROOT: %w", err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return Config{}, fmt.Errorf("create STORAGE_ROOT: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Config{}, fmt.Errorf("evaluate STORAGE_ROOT: %w", err)
	}
	cfg.StorageRoot = filepath.Clean(root)

	if cfg.HostPathPrefix == "" {
		cfg.HostPathPrefix = cfg.StorageRoot
	}
	if cfg.MaxUploadSize <= 0 {
		return Config{}, fmt.Errorf("MAX_UPLOAD_SIZE must be greater than zero")
	}
	if cfg.UploadChunkSize < 1024*1024 {
		return Config{}, fmt.Errorf("UPLOAD_CHUNK_SIZE must be at least 1 MiB")
	}
	if cfg.UploadChunkSize > 64*1024*1024 {
		return Config{}, fmt.Errorf("UPLOAD_CHUNK_SIZE must not exceed 64 MiB")
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
