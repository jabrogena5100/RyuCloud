// Package config loads runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
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
	QueueBackend      string // "disabled", "memory", "redis" or "sqs"
	StorageBackend    string // "disabled" or "s3"
	PlaybackBackend   string // "api" (the API streams media, local) or "cloudfront" (signed CDN access)
	S3                S3Config
	SQS               SQSConfig
	CloudFront        CloudFrontConfig
	Upload            UploadConfig
	Redis             RedisConfig
	Worker            WorkerConfig
}

// RedisConfig configures the job queue transport used when QUEUE_BACKEND=redis.
type RedisConfig struct {
	URL string // e.g. redis://redis:6379/0
}

// SQSConfig configures the AWS SQS queue used when QUEUE_BACKEND=sqs. Credentials come
// from the AWS default chain (the ECS task role); there are no static keys.
type SQSConfig struct {
	QueueURL string // https://sqs.<region>.amazonaws.com/<account>/<name>
	Region   string // defaults to AWS_REGION, then S3_REGION
	// VisibilityTimeout is how long a received message stays hidden. It should exceed the
	// worker's job timeout so a running job is not delivered to a second worker.
	VisibilityTimeout time.Duration
}

// CloudFrontConfig configures private media delivery when PLAYBACK_BACKEND=cloudfront.
// Media is served from a CloudFront distribution whose S3 origin is private. Viewers
// get short-lived signed cookies (playback) and signed URLs (thumbnails) from the API.
type CloudFrontConfig struct {
	Domain       string        // media host, e.g. media.example.com (no scheme)
	KeyPairID    string        // ID of the CloudFront public key in the trusted key group
	PrivateKey   string        // PEM-encoded RSA private key; comes from a secret store, never a file in Git
	CookieDomain string        // parent domain shared with the app, e.g. example.com
	PlaybackTTL  time.Duration // lifetime of the signed playback cookies
	ThumbnailTTL time.Duration // lifetime of signed thumbnail URLs
}

// WorkerConfig tunes the video-processing worker. The API reads MaxAttempts too,
// because it stamps new jobs with it.
type WorkerConfig struct {
	Concurrency   int           // jobs processed at once (each runs FFmpeg)
	MaxAttempts   int           // total tries per job before it is failed for good
	RetryBase     time.Duration // first retry delay; doubles each attempt
	RetryMax      time.Duration // cap on the retry delay
	Lease         time.Duration // how long a claimed job stays owned without a heartbeat
	SweepInterval time.Duration // how often to look for due or abandoned jobs
	RepublishAge  time.Duration // do not re-send a job to the queue more often than this
	ShutdownGrace time.Duration // how long in-flight jobs may finish after SIGTERM
	Timeout       time.Duration // hard limit for processing one job
	MaxDuration   time.Duration // longest source video accepted
	FFmpegThreads int           // threads per FFmpeg process
	FFmpegPath    string
	FFprobePath   string
	TempDir       string
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
		PlaybackBackend:   getenv("PLAYBACK_BACKEND", "api"),
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
		// Cloud deployments inject the pieces separately because the password comes from a
		// secret store; assemble the URL here so the password is escaped correctly.
		cfg.DatabaseURL = databaseURLFromParts()
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL (or DB_HOST, DB_NAME, DB_USER and DB_PASSWORD) is required")
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

	cfg.Redis = RedisConfig{URL: os.Getenv("REDIS_URL")}
	switch cfg.QueueBackend {
	case "disabled", "memory":
	case "redis":
		if u, err := url.Parse(cfg.Redis.URL); err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Host == "" {
			return Config{}, errors.New("REDIS_URL must be a redis:// or rediss:// URL when QUEUE_BACKEND=redis")
		}
	case "sqs":
		if err := loadSQS(&cfg); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("QUEUE_BACKEND must be \"disabled\", \"memory\", \"redis\" or \"sqs\", got %q", cfg.QueueBackend)
	}

	if cfg.Worker, err = loadWorker(); err != nil {
		return Config{}, err
	}

	if err := loadStorage(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadPlayback(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// databaseURLFromParts builds a postgres:// URL from DB_HOST, DB_PORT, DB_NAME, DB_USER,
// DB_PASSWORD and DB_SSLMODE. It returns "" unless the host is set.
func databaseURLFromParts() string {
	host := os.Getenv("DB_HOST")
	if host == "" {
		return ""
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")),
		Host:     net.JoinHostPort(host, getenv("DB_PORT", "5432")),
		Path:     "/" + os.Getenv("DB_NAME"),
		RawQuery: url.Values{"sslmode": {getenv("DB_SSLMODE", "require")}}.Encode(),
	}
	return u.String()
}

func loadSQS(cfg *Config) error {
	region := getenv("SQS_REGION", getenv("AWS_REGION", cfg.S3.Region))
	u, err := url.Parse(os.Getenv("SQS_QUEUE_URL"))
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return errors.New("SQS_QUEUE_URL must be the https:// URL of the queue when QUEUE_BACKEND=sqs")
	}
	// The visibility timeout must outlast a job, or a second worker receives the message
	// while the first is still transcoding (harmless, but wasteful).
	secs, err := intRange("SQS_VISIBILITY_TIMEOUT_SECONDS", 1860, 30, 43200)
	if err != nil {
		return err
	}
	cfg.SQS = SQSConfig{QueueURL: u.String(), Region: region, VisibilityTimeout: time.Duration(secs) * time.Second}
	return nil
}

// loadPlayback validates how media reaches viewers. The default ("api") keeps the local
// behavior; "cloudfront" requires the full signing configuration so a half-configured
// deployment fails at startup instead of serving broken links.
func loadPlayback(cfg *Config) error {
	switch cfg.PlaybackBackend {
	case "api":
		return nil
	case "cloudfront":
	default:
		return fmt.Errorf("PLAYBACK_BACKEND must be \"api\" or \"cloudfront\", got %q", cfg.PlaybackBackend)
	}
	cf := CloudFrontConfig{
		Domain:       strings.TrimSpace(os.Getenv("CLOUDFRONT_DOMAIN")),
		KeyPairID:    strings.TrimSpace(os.Getenv("CLOUDFRONT_KEY_PAIR_ID")),
		PrivateKey:   os.Getenv("CLOUDFRONT_PRIVATE_KEY"),
		CookieDomain: strings.TrimPrefix(strings.TrimSpace(os.Getenv("CLOUDFRONT_COOKIE_DOMAIN")), "."),
	}
	if cf.Domain == "" || strings.Contains(cf.Domain, "/") || strings.Contains(cf.Domain, ":") {
		return errors.New("CLOUDFRONT_DOMAIN must be a bare host name (for example media.example.com) when PLAYBACK_BACKEND=cloudfront")
	}
	if cf.KeyPairID == "" || cf.PrivateKey == "" {
		return errors.New("CLOUDFRONT_KEY_PAIR_ID and CLOUDFRONT_PRIVATE_KEY are required when PLAYBACK_BACKEND=cloudfront")
	}
	// Browsers only send the signed cookies to the media host if they are scoped to a
	// parent domain shared by the app and the CDN.
	if cf.CookieDomain == "" || (cf.Domain != cf.CookieDomain && !strings.HasSuffix(cf.Domain, "."+cf.CookieDomain)) {
		return errors.New("CLOUDFRONT_COOKIE_DOMAIN must be a parent domain of CLOUDFRONT_DOMAIN (for example example.com)")
	}
	if !cfg.CookieSecure {
		return errors.New("PLAYBACK_BACKEND=cloudfront requires COOKIE_SECURE=true")
	}
	mins, err := intRange("CLOUDFRONT_PLAYBACK_TTL_MINUTES", 120, 5, 720)
	if err != nil {
		return err
	}
	thumbMins, err := intRange("CLOUDFRONT_THUMBNAIL_TTL_MINUTES", 60, 1, 720)
	if err != nil {
		return err
	}
	cf.PlaybackTTL = time.Duration(mins) * time.Minute
	cf.ThumbnailTTL = time.Duration(thumbMins) * time.Minute
	cfg.CloudFront = cf
	return nil
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

// intRange reads an integer environment variable that must be within [minVal, maxVal].
func intRange(key string, fallback, minVal, maxVal int) (int, error) {
	n, err := strconv.Atoi(getenv(key, strconv.Itoa(fallback)))
	if err != nil || n < minVal || n > maxVal {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minVal, maxVal)
	}
	return n, nil
}

// loadWorker reads and validates the worker settings. Durations are given in
// seconds (or minutes where noted) so they are easy to override in Compose.
func loadWorker() (WorkerConfig, error) {
	var w WorkerConfig
	seconds := func(key string, fallback, minVal, maxVal int, dst *time.Duration) error {
		n, err := intRange(key, fallback, minVal, maxVal)
		if err != nil {
			return err
		}
		*dst = time.Duration(n) * time.Second
		return nil
	}

	var err error
	if w.Concurrency, err = intRange("WORKER_CONCURRENCY", 1, 1, 16); err != nil {
		return w, err
	}
	if w.MaxAttempts, err = intRange("WORKER_MAX_ATTEMPTS", 4, 1, 20); err != nil {
		return w, err
	}
	if w.FFmpegThreads, err = intRange("FFMPEG_THREADS", 2, 1, 32); err != nil {
		return w, err
	}
	for _, f := range []struct {
		key                string
		fallback, min, max int
		dst                *time.Duration
	}{
		{"WORKER_RETRY_BASE_SECONDS", 30, 1, 3600, &w.RetryBase},
		{"WORKER_RETRY_MAX_SECONDS", 900, 1, 86400, &w.RetryMax},
		{"WORKER_LEASE_SECONDS", 120, 10, 3600, &w.Lease},
		{"WORKER_SWEEP_SECONDS", 15, 1, 600, &w.SweepInterval},
		{"WORKER_REPUBLISH_SECONDS", 300, 1, 86400, &w.RepublishAge},
		{"WORKER_SHUTDOWN_GRACE_SECONDS", 60, 0, 3600, &w.ShutdownGrace},
		{"WORKER_JOB_TIMEOUT_SECONDS", 1800, 30, 86400, &w.Timeout},
	} {
		if err := seconds(f.key, f.fallback, f.min, f.max, f.dst); err != nil {
			return w, err
		}
	}
	if w.RetryMax < w.RetryBase {
		return w, errors.New("WORKER_RETRY_MAX_SECONDS must not be smaller than WORKER_RETRY_BASE_SECONDS")
	}
	minutes, err := intRange("WORKER_MAX_DURATION_MINUTES", 120, 1, 1440)
	if err != nil {
		return w, err
	}
	w.MaxDuration = time.Duration(minutes) * time.Minute

	w.FFmpegPath = getenv("FFMPEG_PATH", "ffmpeg")
	w.FFprobePath = getenv("FFPROBE_PATH", "ffprobe")
	w.TempDir = getenv("WORKER_TEMP_DIR", os.TempDir())
	return w, nil
}
