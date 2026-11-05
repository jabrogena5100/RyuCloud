package config

import (
	"testing"
	"time"
)

// clearEnv blanks variables that Docker Compose sets, so tests see code defaults.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"COOKIE_SECURE", "SESSION_TTL_HOURS", "TRUSTED_ORIGINS", "QUEUE_BACKEND", "ENABLE_API_DOCS",
		"STORAGE_BACKEND", "S3_ENDPOINT", "S3_PUBLIC_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY_ID",
		"S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE", "UPLOAD_MAX_BYTES", "UPLOAD_URL_TTL_MINUTES",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://example")
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error when DATABASE_URL is empty")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CookieSecure {
		t.Error("COOKIE_SECURE should default to true")
	}
	if cfg.SessionTTL != 168*time.Hour {
		t.Errorf("SessionTTL = %v, want 168h", cfg.SessionTTL)
	}
	if len(cfg.TrustedOrigins) != 1 || cfg.TrustedOrigins[0] != "http://localhost:3000" {
		t.Errorf("TrustedOrigins = %v", cfg.TrustedOrigins)
	}
}

func TestLoadParsesTrustedOrigins(t *testing.T) {
	clearEnv(t)
	t.Setenv("TRUSTED_ORIGINS", "http://a.test/, http://b.test ,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedOrigins) != 2 || cfg.TrustedOrigins[0] != "http://a.test" || cfg.TrustedOrigins[1] != "http://b.test" {
		t.Errorf("TrustedOrigins = %v", cfg.TrustedOrigins)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"SESSION_TTL_HOURS":      "0",
		"COOKIE_SECURE":          "maybe",
		"QUEUE_BACKEND":          "sqs",
		"STORAGE_BACKEND":        "gcs",
		"UPLOAD_MAX_BYTES":       "10",
		"UPLOAD_URL_TTL_MINUTES": "0",
		"S3_USE_PATH_STYLE":      "sometimes",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("expected an error for %s=%q", key, value)
			}
		})
	}
}

func TestLoadStorageDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageBackend != "disabled" {
		t.Errorf("StorageBackend = %q, want disabled", cfg.StorageBackend)
	}
	if cfg.Upload.MaxBytes != 1<<30 || cfg.Upload.URLTTL != 30*time.Minute {
		t.Errorf("Upload = %+v", cfg.Upload)
	}
}

func s3Env(t *testing.T) {
	t.Helper()
	clearEnv(t)
	t.Setenv("STORAGE_BACKEND", "s3")
	t.Setenv("S3_BUCKET", "media")
	t.Setenv("S3_ENDPOINT", "http://minio:9000/")
	t.Setenv("S3_PUBLIC_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "app")
	t.Setenv("S3_SECRET_ACCESS_KEY", "app-secret")
}

func TestLoadS3(t *testing.T) {
	s3Env(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.Endpoint != "http://minio:9000" || cfg.S3.PublicEndpoint != "http://localhost:9000" {
		t.Errorf("endpoints = %q / %q", cfg.S3.Endpoint, cfg.S3.PublicEndpoint)
	}
	if !cfg.S3.UsePathStyle {
		t.Error("path-style addressing should default to true for a custom endpoint")
	}
	if cfg.S3.Region != "us-east-1" {
		t.Errorf("Region = %q", cfg.S3.Region)
	}
}

func TestLoadS3PublicEndpointDefaultsToEndpoint(t *testing.T) {
	s3Env(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.PublicEndpoint != "http://minio:9000" {
		t.Errorf("PublicEndpoint = %q", cfg.S3.PublicEndpoint)
	}
}

func TestLoadS3AWSDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("STORAGE_BACKEND", "s3")
	t.Setenv("S3_BUCKET", "media")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("real AWS needs only a bucket: %v", err)
	}
	if cfg.S3.UsePathStyle || cfg.S3.Endpoint != "" {
		t.Errorf("unexpected S3 config for AWS: %+v", cfg.S3)
	}
}

func TestLoadS3Validation(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"missing bucket":         func(t *testing.T) { t.Setenv("S3_BUCKET", "") },
		"only access key":        func(t *testing.T) { t.Setenv("S3_SECRET_ACCESS_KEY", "") },
		"endpoint without http":  func(t *testing.T) { t.Setenv("S3_ENDPOINT", "minio:9000") },
		"public endpoint is bad": func(t *testing.T) { t.Setenv("S3_PUBLIC_ENDPOINT", "ftp://localhost") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s3Env(t)
			mutate(t)
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
