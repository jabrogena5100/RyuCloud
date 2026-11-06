// Package integrations defines the seams for external services: object storage
// (MinIO locally, AWS S3 in production), a job queue (Redis locally, SQS in AWS) and
// playback URLs (the API locally, CloudFront in AWS). Handlers depend only on these interfaces.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

var (
	// ErrNotConfigured is returned by stand-ins that refuse to pretend they work.
	ErrNotConfigured = errors.New("integration not configured")
	// ErrObjectNotFound is returned by StatObject when the key does not exist.
	ErrObjectNotFound = errors.New("object not found")
)

// PresignRequest describes a single-object upload the browser may perform.
type PresignRequest struct {
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	TTL         time.Duration
}

// PresignedUpload is everything the browser needs to upload directly to object
// storage: send a request with Method to URL, with exactly these Headers and a
// body of exactly the declared size. Video bytes never pass through the API.
type PresignedUpload struct {
	Method    string
	URL       string
	Headers   map[string]string
	ObjectKey string
	ExpiresAt time.Time
}

// ObjectInfo is the metadata of a stored object, as reported by the store itself.
type ObjectInfo struct {
	Size        int64
	ContentType string
	ETag        string
}

// ObjectReader is an open object. The caller must close Body.
type ObjectReader struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// ObjectStorage stores raw uploads, HLS renditions and thumbnails.
type ObjectStorage interface {
	// PresignUpload returns a time-limited URL that allows uploading exactly one object.
	PresignUpload(ctx context.Context, req PresignRequest) (PresignedUpload, error)
	// StatObject returns the object's real size and content type, or ErrObjectNotFound.
	StatObject(ctx context.Context, objectKey string) (ObjectInfo, error)
	// DeleteObject removes an object. Deleting a missing object is not an error.
	DeleteObject(ctx context.Context, objectKey string) error
	// GetObject opens an object for reading, or returns ErrObjectNotFound.
	GetObject(ctx context.Context, objectKey string) (ObjectReader, error)
	// PutObject stores size bytes read from body (the worker's transcoding output).
	PutObject(ctx context.Context, objectKey, contentType string, body io.ReadSeeker, size int64) error
	// DeletePrefix removes every object whose key starts with prefix and returns how many.
	DeletePrefix(ctx context.Context, prefix string) (int, error)
}

// Key layout. Everything lives in one private bucket; nothing is public.
//
//	uploads/{videoID}/{uploadID}/source     original upload (kept; never served)
//	videos/{videoID}/hls/master.m3u8        HLS master playlist
//	videos/{videoID}/hls/{360p}/index.m3u8  media playlist of one rendition
//	videos/{videoID}/hls/{360p}/seg_00000.ts
//	videos/{videoID}/thumbnail.jpg
//
// Derived keys depend only on the video ID, so reprocessing overwrites the same
// keys instead of accumulating duplicates.

// VideoPrefix is the key prefix of everything generated for a video.
func VideoPrefix(videoID string) string { return "videos/" + videoID + "/" }

// HLSKey is the key of a file inside a video's HLS output (rel like "360p/index.m3u8").
func HLSKey(videoID, rel string) string { return VideoPrefix(videoID) + "hls/" + rel }

// HLSMasterKey is the key of a video's master playlist.
func HLSMasterKey(videoID string) string { return HLSKey(videoID, "master.m3u8") }

// ThumbnailKey is the key of a video's generated thumbnail.
func ThumbnailKey(videoID string) string { return VideoPrefix(videoID) + "thumbnail.jpg" }

// SourceUploadKey is the key for one upload attempt of a video's source file.
// Both IDs are server-generated UUIDs and the client's filename is never part of
// the key, so keys cannot collide or be steered by user input. Processed
// renditions will live under a separate prefix.
func SourceUploadKey(videoID, uploadID string) string {
	return "uploads/" + videoID + "/" + uploadID + "/source"
}

// DisabledStorage fails loudly instead of pretending uploads work.
type DisabledStorage struct{}

func (DisabledStorage) PresignUpload(context.Context, PresignRequest) (PresignedUpload, error) {
	return PresignedUpload{}, ErrNotConfigured
}

func (DisabledStorage) StatObject(context.Context, string) (ObjectInfo, error) {
	return ObjectInfo{}, ErrNotConfigured
}

func (DisabledStorage) DeleteObject(context.Context, string) error { return ErrNotConfigured }

func (DisabledStorage) GetObject(context.Context, string) (ObjectReader, error) {
	return ObjectReader{}, ErrNotConfigured
}

func (DisabledStorage) PutObject(context.Context, string, string, io.ReadSeeker, int64) error {
	return ErrNotConfigured
}

func (DisabledStorage) DeletePrefix(context.Context, string) (int, error) { return 0, ErrNotConfigured }

// NewStorage returns the storage backend selected by STORAGE_BACKEND.
func NewStorage(ctx context.Context, cfg config.Config) (ObjectStorage, error) {
	switch cfg.StorageBackend {
	case "s3":
		return NewS3Storage(ctx, cfg.S3)
	case "disabled", "":
		return DisabledStorage{}, nil
	default:
		return nil, fmt.Errorf("unknown storage backend %q", cfg.StorageBackend)
	}
}

// NewQueue returns the queue selected by QUEUE_BACKEND: "redis" (local development),
// "sqs" (AWS), "memory" (tests and single-process experiments) or "disabled".
func NewQueue(ctx context.Context, cfg config.Config) (Queue, error) {
	switch cfg.QueueBackend {
	case "redis":
		return NewRedisQueue(ctx, cfg.Redis.URL)
	case "sqs":
		return NewSQSQueue(ctx, cfg.SQS)
	case "memory":
		return NewMemoryQueue(), nil
	case "disabled", "":
		return DisabledQueue{}, nil
	default:
		return nil, fmt.Errorf("unknown queue backend %q", cfg.QueueBackend)
	}
}
