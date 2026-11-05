// Package integrations defines the seams for external services: object storage
// (MinIO locally, AWS S3 later), a job queue (SQS, Phase 3) and playback URLs
// (CloudFront, Phase 4). Handlers depend only on these interfaces.
package integrations

import (
	"context"
	"errors"
	"fmt"
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

// ObjectStorage stores raw uploads (and, in later phases, renditions and thumbnails).
type ObjectStorage interface {
	// PresignUpload returns a time-limited URL that allows uploading exactly one object.
	PresignUpload(ctx context.Context, req PresignRequest) (PresignedUpload, error)
	// StatObject returns the object's real size and content type, or ErrObjectNotFound.
	StatObject(ctx context.Context, objectKey string) (ObjectInfo, error)
	// DeleteObject removes an object. Deleting a missing object is not an error.
	DeleteObject(ctx context.Context, objectKey string) error
}

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
