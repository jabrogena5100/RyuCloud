// Package config loads runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime settings. Use Load to read them from the environment.
type Config struct {
	Environment       string
	Port              string
	LogLevel          string
	DatabaseURL       string
	SessionCookieName string
	SessionTTL        time.Duration
	CookieSecure      bool
	TrustedOrigins    []string
	EnableAPIDocs     bool
	QueueBackend      string
	StorageBackend    string // "disabled" or "s3"
	S3                S3Config
	Upload            UploadConfig
}

// S3Config configures the S3-compatible object store: MinIO locally, AWS S3 later.
// With real AWS, leave Endpoint empty and the credentials unset, so the SDK uses
// its default credential chain (for example an IAM role).
type S3Config struct {
	// Endpoint is how the API itself reaches the store (inside Docker: http://minio:9000).
	Endpoint string
	// PublicEndpoint is what browsers use. Presigned URLs embed it, and it is part of
	// the request signature, so it must be reachable from the browser (http://localhost:9000).
	// Defaults to Endpoint.
	PublicEndpoint  string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
}

// UploadConfig limits what the upload API accepts.
type UploadConfig struct {
	MaxBytes int64         // largest accepted file; one presigned PUT is limited to 5 GiB
	URLTTL   time.Duration // how long a presigned upload URL stays valid
}

const maxSessionTTLHours = 24 * 90

// Upload limits. One presigned PUT can carry at most 5 GiB (the S3 single-request limit).
const minUploadBytes = 1 << 20
const maxUploadBytes = 5 << 30
const defaultUploadBytes = 1 << 30
const maxUploadURLTTLMins = 60
const defaultUploadURLMins = 30

// Load reads the configuration. DATABASE_URL is required; there is deliberately
// no default containing credentials.
func Load() (Config, error) {
	cfg := Config{
		Environment:       getenv("ENVIRONMENT", "development"),
		Port:              getenv("PORT", "8000"),
		LogLevel:          getenv("LOG_LEVEL", "INFO"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		SessionCookieName: getenv("SESSION_COOKIE_NAME", "sf_session"),
		TrustedOrigins:    splitOrigins(getenv("TRUSTED_ORIGINS", "http://localhost:3000")),
		QueueBackend:      getenv("QUEUE_BACKEND", "disabled"),
		StorageBackend:    getenv("STORAGE_BACKEND", "disabled"),
	}
	cfg.S3 = S3Config{
		Endpoint:        strings.TrimRight(os.Getenv("S3_ENDPOINT"), "/"),
		PublicEndpoint:  strings.TrimRight(os.Getenv("S3_PUBLIC_ENDPOINT"), "/"),
		Region:          getenv("S3_REGION", "us-east-1"),
		Bucket:          os.Getenv("S3_BUCKET"),
		AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	var err error
	// Secure by default; local HTTP development must opt out explicitly.
	if cfg.CookieSecure, err = getbool("COOKIE_SECURE", true); err != nil {
		return Config{}, err
	}
	if cfg.EnableAPIDocs, err = getbool("ENABLE_API_DOCS", true); err != nil {
		return Config{}, err
	}

	hours, err := strconv.Atoi(getenv("SESSION_TTL_HOURS", "168"))
	if err != nil || hours < 1 || hours > maxSessionTTLHours {
		return Config{}, fmt.Errorf("SESSION_TTL_HOURS must be an integer from 1 to %d", maxSessionTTLHours)
	}
	cfg.SessionTTL = time.Duration(hours) * time.Hour

	switch cfg.QueueBackend {
	case "disabled", "memory":
	default:
		return Config{}, fmt.Errorf("QUEUE_BACKEND must be \"disabled\" or \"memory\", got %q", cfg.QueueBackend)
	}

	if err := loadStorage(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadStorage reads and validates the storage and upload settings.
func loadStorage(cfg *Config) error {
	var err error
	if cfg.S3.UsePathStyle, err = getbool("S3_USE_PATH_STYLE", cfg.S3.Endpoint != ""); err != nil {
		return err
	}
	if cfg.S3.PublicEndpoint == "" {
		cfg.S3.PublicEndpoint = cfg.S3.Endpoint
	}

	maxBytes, err := strconv.ParseInt(getenv("UPLOAD_MAX_BYTES", strconv.Itoa(defaultUploadBytes)), 10, 64)
	if err != nil || maxBytes < minUploadBytes || maxBytes > maxUploadBytes {
		return fmt.Errorf("UPLOAD_MAX_BYTES must be an integer from %d to %d", minUploadBytes, int64(maxUploadBytes))
	}
	minutes, err := strconv.Atoi(getenv("UPLOAD_URL_TTL_MINUTES", strconv.Itoa(defaultUploadURLMins)))
	if err != nil || minutes < 1 || minutes > maxUploadURLTTLMins {
		return fmt.Errorf("UPLOAD_URL_TTL_MINUTES must be an integer from 1 to %d", maxUploadURLTTLMins)
	}
	cfg.Upload = UploadConfig{MaxBytes: maxBytes, URLTTL: time.Duration(minutes) * time.Minute}

	switch cfg.StorageBackend {
	case "disabled":
		return nil
	case "s3":
	default:
		return fmt.Errorf("STORAGE_BACKEND must be \"disabled\" or \"s3\", got %q", cfg.StorageBackend)
	}

	if cfg.S3.Bucket == "" {
		return errors.New("S3_BUCKET is required when STORAGE_BACKEND=s3")
	}
	if (cfg.S3.AccessKeyID == "") != (cfg.S3.SecretAccessKey == "") {
		return errors.New("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together (or both left empty to use the AWS default credential chain)")
	}
	for name, value := range map[string]string{"S3_ENDPOINT": cfg.S3.Endpoint, "S3_PUBLIC_ENDPOINT": cfg.S3.PublicEndpoint} {
		if value == "" {
			continue // empty means the AWS default endpoint
		}
		if u, err := url.Parse(value); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be an http(s) URL, got %q", name, value)
		}
	}
	return nil
}

// getenv treats an empty variable the same as an unset one.
func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getbool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", key, value)
	}
	return b, nil
}

func splitOrigins(value string) []string {
	var origins []string
	for _, origin := range strings.Split(value, ",") {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}
