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
		"REDIS_URL", "WORKER_CONCURRENCY", "WORKER_MAX_ATTEMPTS", "FFMPEG_THREADS", "WORKER_RETRY_BASE_SECONDS",
		"WORKER_RETRY_MAX_SECONDS", "WORKER_LEASE_SECONDS", "WORKER_SWEEP_SECONDS", "WORKER_REPUBLISH_SECONDS",
		"WORKER_SHUTDOWN_GRACE_SECONDS", "WORKER_JOB_TIMEOUT_SECONDS", "WORKER_MAX_DURATION_MINUTES",
		"WORKER_TEMP_DIR", "FFMPEG_PATH", "FFPROBE_PATH",
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
		"WORKER_CONCURRENCY":     "0",
		"WORKER_MAX_ATTEMPTS":    "many",
		"WORKER_LEASE_SECONDS":   "1",
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

func TestLoadWorkerDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Worker
	if w.Concurrency != 1 || w.MaxAttempts != 4 || w.FFmpegThreads != 2 {
		t.Errorf("unexpected worker defaults: %+v", w)
	}
	if w.RetryBase != 30*time.Second || w.RetryMax != 15*time.Minute || w.Lease != 2*time.Minute {
		t.Errorf("unexpected timing defaults: %+v", w)
	}
	if w.Timeout != 30*time.Minute || w.MaxDuration != 2*time.Hour || w.FFmpegPath != "ffmpeg" {
		t.Errorf("unexpected limits: %+v", w)
	}
}

func TestLoadRedisQueue(t *testing.T) {
	clearEnv(t)
	t.Setenv("QUEUE_BACKEND", "redis")
	t.Setenv("REDIS_URL", "redis://redis:6379/0")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueueBackend != "redis" || cfg.Redis.URL != "redis://redis:6379/0" {
		t.Errorf("unexpected queue config: %+v %+v", cfg.QueueBackend, cfg.Redis)
	}

	for _, bad := range []string{"", "localhost:6379", "http://redis"} {
		t.Setenv("REDIS_URL", bad)
		if _, err := Load(); err == nil {
			t.Errorf("REDIS_URL=%q should be rejected", bad)
		}
	}
}

func TestLoadWorkerRetryMaxMustNotBeBelowBase(t *testing.T) {
	clearEnv(t)
	t.Setenv("WORKER_RETRY_BASE_SECONDS", "60")
	t.Setenv("WORKER_RETRY_MAX_SECONDS", "30")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error")
	}
}
