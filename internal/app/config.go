package app

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultChunkSize     = int64(2 * 1024 * 1024)
	defaultMaxUploadSize = int64(100 * 1024 * 1024 * 1024)
)

type Config struct {
	ListenAddr         string
	StorageRoot        string
	HostPathPrefix     string
	Password           string
	CookieSecure       bool
	MaxUploadSize      int64
	UploadChunkSize    int64
	LANTransferOrigin  string
	STUNTransferDomain string
	STUNWebhookToken   string
	TransferSigningKey string
}

func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr:         envOrDefault("LISTEN_ADDR", ":8080"),
		StorageRoot:        envOrDefault("STORAGE_ROOT", "/data"),
		HostPathPrefix:     strings.TrimSpace(os.Getenv("HOST_PATH_PREFIX")),
		Password:           os.Getenv("APP_PASSWORD"),
		CookieSecure:       envBool("COOKIE_SECURE", false),
		MaxUploadSize:      envInt64("MAX_UPLOAD_SIZE", defaultMaxUploadSize),
		UploadChunkSize:    envInt64("UPLOAD_CHUNK_SIZE", defaultChunkSize),
		LANTransferOrigin:  strings.TrimSpace(os.Getenv("LAN_TRANSFER_ORIGIN")),
		STUNTransferDomain: strings.TrimSpace(os.Getenv("STUN_TRANSFER_DOMAIN")),
		STUNWebhookToken:   strings.TrimSpace(os.Getenv("STUN_WEBHOOK_TOKEN")),
		TransferSigningKey: strings.TrimSpace(os.Getenv("TRANSFER_SIGNING_KEY")),
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
	transferValues := 0
	for _, value := range []string{
		cfg.STUNTransferDomain,
		cfg.STUNWebhookToken,
		cfg.TransferSigningKey,
	} {
		if value != "" {
			transferValues++
		}
	}
	if transferValues != 0 && transferValues != 3 {
		return Config{}, fmt.Errorf("STUN_TRANSFER_DOMAIN, STUN_WEBHOOK_TOKEN and TRANSFER_SIGNING_KEY must be configured together")
	}
	if transferValues == 3 {
		if !validTransferDomain(cfg.STUNTransferDomain) {
			return Config{}, fmt.Errorf("STUN_TRANSFER_DOMAIN must be a hostname without scheme, path or port")
		}
		if len(cfg.STUNWebhookToken) < 24 {
			return Config{}, fmt.Errorf("STUN_WEBHOOK_TOKEN must contain at least 24 characters")
		}
		if len(cfg.TransferSigningKey) < 32 {
			return Config{}, fmt.Errorf("TRANSFER_SIGNING_KEY must contain at least 32 characters")
		}
	}
	if cfg.LANTransferOrigin != "" {
		if transferValues != 3 {
			return Config{}, fmt.Errorf("LAN_TRANSFER_ORIGIN requires STUN transfer signing configuration")
		}
		origin, err := normalizeLANTransferOrigin(cfg.LANTransferOrigin)
		if err != nil {
			return Config{}, err
		}
		cfg.LANTransferOrigin = origin
	}

	return cfg, nil
}

func normalizeLANTransferOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", fmt.Errorf("LAN_TRANSFER_ORIGIN must be an HTTPS origin without path, query or fragment")
	}
	if !validTransferDomain(parsed.Hostname()) {
		return "", fmt.Errorf("LAN_TRANSFER_ORIGIN hostname is invalid")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("LAN_TRANSFER_ORIGIN port is invalid")
		}
	}
	return "https://" + parsed.Host, nil
}

func validTransferDomain(value string) bool {
	if value == "" || strings.ContainsAny(value, "/:@?#[]") || len(value) > 253 {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return false
			}
		}
	}
	return true
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
