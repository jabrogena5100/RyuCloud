// Package uploads implements the direct-to-storage video upload lifecycle.
//
//	POST   /uploads                 validate metadata, create video + upload rows, return a presigned PUT URL
//	(browser PUTs the file straight to object storage; the API never sees the bytes)
//	POST   /uploads/{id}/complete   verify the stored object, mark the upload "uploaded"
//	GET    /uploads/{id}            status
//	DELETE /uploads/{id}            cancel
//
// Upload states: pending -> uploaded | failed | cancelled | expired. Only
// "pending" has outgoing transitions, and every transition is a conditional
// UPDATE ("... WHERE status = 'pending'"), so repeated, concurrent or racing
// requests (complete vs. cancel, double-clicks, retries) are safe and idempotent.
//
// A verified upload moves the video to "processing", meaning "queued for the
// transcoding worker that arrives in Phase 3". Nothing is playable before then:
// videos only become public via status "ready".
package uploads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/channels"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
)

// Upload states; the database enforces them with a CHECK constraint.
const (
	StatusPending   = "pending"
	StatusUploaded  = "uploaded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusExpired   = "expired"
)

// completionGrace is how long after the presigned URL expires a client may still
// report completion. A request signed at the last moment can legitimately take a
// long time to transfer a large file, so the completion deadline is later than
// the URL's own expiry.
const completionGrace = 2 * time.Hour

// maxPendingPerUser caps in-flight uploads per user so an account cannot create
// unlimited presigned URLs.
const maxPendingPerUser = 10

const (
	reasonExpired  = "The upload window elapsed before the file was confirmed."
	reasonMismatch = "The uploaded file did not match the declared size or type."
)

var (
	errNotFound             = httpx.NewError(http.StatusNotFound, "upload_not_found", "Upload not found.")
	errChannelForbidden     = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this channel.")
	errTooManyPending       = httpx.NewError(http.StatusTooManyRequests, "too_many_uploads", "Too many uploads in progress. Finish or cancel one first.")
	errIncomplete           = httpx.NewError(http.StatusConflict, "upload_incomplete", "The file has not been uploaded yet.")
	errCancelled            = httpx.NewError(http.StatusConflict, "upload_cancelled", "This upload was cancelled.")
	errFailed               = httpx.NewError(http.StatusConflict, "upload_failed", "This upload failed verification.")
	errExpired              = httpx.NewError(http.StatusGone, "upload_expired", "This upload expired. Start a new upload.")
	errAlreadyCompleted     = httpx.NewError(http.StatusConflict, "upload_already_completed", "This upload is already complete and cannot be cancelled.")
	errRejected             = httpx.NewError(http.StatusUnprocessableEntity, "upload_rejected", "The uploaded file does not match what was declared, so it was discarded.")
	errStorageNotConfigured = httpx.NewError(http.StatusServiceUnavailable, "storage_not_configured", "Object storage is not configured.")
	errStorageUnavailable   = httpx.NewError(http.StatusBadGateway, "storage_unavailable", "Object storage is unavailable. Please try again.")
)

// Upload is the API representation of one upload attempt.
type Upload struct {
	ID               string     `json:"id"`
	VideoID          string     `json:"video_id"`
	Status           string     `json:"status"`
	OriginalFilename string     `json:"original_filename"`
	ContentType      string     `json:"content_type"`
	SizeBytes        int64      `json:"size_bytes"`
	FailureReason    *string    `json:"failure_reason"`
	ExpiresAt        time.Time  `json:"expires_at"`
	CompletedAt      *time.Time `json:"completed_at"`
	CreatedAt        time.Time  `json:"created_at"`

	objectKey string
}

// VideoSummary is the slice of the new video returned when an upload starts.
type VideoSummary struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Visibility string `json:"visibility"`
}

// Target tells the browser how to upload: send Method to URL with exactly Headers.
type Target struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// Initiated is the response to starting an upload.
type Initiated struct {
	Upload Upload       `json:"upload"`
	Video  VideoSummary `json:"video"`
	Target Target       `json:"target"`
}

// InitiateParams are the validated inputs for starting an upload.
type InitiateParams struct {
	ChannelID   string
	Title       string
	Description string
	Visibility  string
	Filename    string
	ContentType string // normalized and allow-listed
	SizeBytes   int64  // within limits
}

// Options are the upload limits.
type Options struct {
	MaxBytes int64
	URLTTL   time.Duration
}

// Service owns the upload lifecycle. Storage sits behind an interface, so MinIO
// today and AWS S3 later need no changes here.
type Service struct {
	db      *pgxpool.Pool
	storage integrations.ObjectStorage
	opts    Options
}

func NewService(db *pgxpool.Pool, storage integrations.ObjectStorage, opts Options) *Service {
	return &Service{db: db, storage: storage, opts: opts}
}

const uploadColumns = `id, video_id, user_id, object_key, original_filename, content_type,
	declared_size_bytes, status, failure_reason, expires_at, completed_at, created_at`

type scanner interface {
	Scan(dest ...any) error
}

// scanUpload also returns the owning user's ID, which is not part of the API shape.
func scanUpload(row scanner) (Upload, string, error) {
	var (
		u      Upload
		userID string
	)
	err := row.Scan(&u.ID, &u.VideoID, &userID, &u.objectKey, &u.OriginalFilename, &u.ContentType,
		&u.SizeBytes, &u.Status, &u.FailureReason, &u.ExpiresAt, &u.CompletedAt, &u.CreatedAt)
	return u, userID, err
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// storageError converts a storage failure into an API error. Details are logged,
// never returned to the client.
func storageError(ctx context.Context, err error) error {
	if errors.Is(err, integrations.ErrNotConfigured) {
		return errStorageNotConfigured
	}
	slog.ErrorContext(ctx, "object storage call failed", "request_id", httpx.RequestID(ctx), "error", err)
	return errStorageUnavailable
}

// discard deletes an object that will never be used. Failure is only logged:
// the upload's state in the database is what matters, and an orphaned object is
// removed later by a bucket lifecycle rule (see docs/uploads.md).
func (s *Service) discard(ctx context.Context, objectKey string) {
	if err := s.storage.DeleteObject(ctx, objectKey); err != nil && !errors.Is(err, integrations.ErrNotConfigured) {
		slog.WarnContext(ctx, "could not delete upload object", "request_id", httpx.RequestID(ctx), "object_key", objectKey, "error", err)
	}
}

// Initiate validates ownership, creates the video and upload rows in one
// transaction, and returns a presigned upload target. Presigning is a local
// computation (no network call), so if it fails the whole transaction rolls
// back and no rows are left behind.
func (s *Service) Initiate(ctx context.Context, userID string, p InitiateParams) (Initiated, error) {
	var out Initiated
	cutoff := time.Now().Add(-completionGrace)

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var ownerID string
		err := tx.QueryRow(ctx, `SELECT owner_id FROM channels WHERE id = $1`, p.ChannelID).Scan(&ownerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return channels.ErrNotFound
		}
		if err != nil {
			return err
		}
		if ownerID != userID {
			return errChannelForbidden
		}

		// Serialise this user's initiations so the pending-upload cap cannot be raced past.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM video_uploads WHERE user_id = $1 AND status = 'pending' AND expires_at > $2`,
			userID, cutoff,
		).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingPerUser {
			return errTooManyPending
		}

		var videoID, uploadID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO videos (channel_id, title, description, status, visibility)
			 VALUES ($1, $2, $3, 'uploading', $4) RETURNING id`,
			p.ChannelID, p.Title, p.Description, p.Visibility,
		).Scan(&videoID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&uploadID); err != nil {
			return err
		}

		presigned, err := s.storage.PresignUpload(ctx, integrations.PresignRequest{
			ObjectKey:   integrations.SourceUploadKey(videoID, uploadID),
			ContentType: p.ContentType,
			SizeBytes:   p.SizeBytes,
			TTL:         s.opts.URLTTL,
		})
		if err != nil {
			return storageError(ctx, err)
		}

		upload, _, err := scanUpload(tx.QueryRow(ctx,
			`INSERT INTO video_uploads
			   (id, video_id, user_id, object_key, original_filename, content_type, declared_size_bytes, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING `+uploadColumns,
			uploadID, videoID, userID, presigned.ObjectKey, p.Filename, p.ContentType, p.SizeBytes, presigned.ExpiresAt,
		))
		if err != nil {
			return err
		}

		out = Initiated{
			Upload: upload,
			Video:  VideoSummary{ID: videoID, Title: p.Title, Status: "uploading", Visibility: p.Visibility},
			Target: Target{Method: presigned.Method, URL: presigned.URL, Headers: presigned.Headers, ExpiresAt: presigned.ExpiresAt},
		}
		return nil
	})
	if err != nil {
		return Initiated{}, err
	}
	return out, nil
}

// load returns the caller's upload. Someone else's upload is reported as missing,
// so upload IDs cannot be probed.
func (s *Service) load(ctx context.Context, id, userID string) (Upload, error) {
	u, _, err := scanUpload(s.db.QueryRow(ctx,
		`SELECT `+uploadColumns+` FROM video_uploads WHERE id = $1 AND user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Upload{}, errNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	return u, nil
}

// expireStale moves the user's abandoned pending uploads to "expired" (and their
// videos to "failed"), then deletes any partial object. It runs lazily before
// every read or change, so no background job is needed in Phase 2.
func (s *Service) expireStale(ctx context.Context, userID string) error {
	type stale struct{ videoID, objectKey string }
	var expired []stale
	cutoff := time.Now().Add(-completionGrace)

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE video_uploads SET status = 'expired', failure_reason = $3, updated_at = now()
			 WHERE user_id = $1 AND status = 'pending' AND expires_at < $2
			 RETURNING video_id, object_key`,
			userID, cutoff, reasonExpired)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st stale
			if err := rows.Scan(&st.videoID, &st.objectKey); err != nil {
				rows.Close()
				return err
			}
			expired = append(expired, st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, st := range expired {
			if _, err := tx.Exec(ctx,
				`UPDATE videos SET status = 'failed', updated_at = now() WHERE id = $1 AND status = 'uploading'`,
				st.videoID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, st := range expired {
		s.discard(ctx, st.objectKey)
	}
	return nil
}

// terminate moves a pending upload to a final state and updates its video in
// the same transaction. changed is false when the upload was no longer pending
// (another request got there first).
func (s *Service) terminate(ctx context.Context, id, status, reason, videoStatus string) (objectKey string, changed bool, err error) {
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var videoID string
		e := tx.QueryRow(ctx,
			`UPDATE video_uploads SET status = $2, failure_reason = $3, updated_at = now()
			 WHERE id = $1 AND status = 'pending'
			 RETURNING video_id, object_key`,
			id, status, reasonArg).Scan(&videoID, &objectKey)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		changed = true
		_, e = tx.Exec(ctx,
			`UPDATE videos SET status = $2, updated_at = now() WHERE id = $1 AND status = 'uploading'`,
			videoID, videoStatus)
		return e
	})
	return objectKey, changed, err
}

// completeOutcome maps a non-pending state to the result of a complete request.
func completeOutcome(up Upload) (Upload, error) {
	switch up.Status {
	case StatusUploaded:
		return up, nil // idempotent: completing twice succeeds with the same result
	case StatusCancelled:
		return Upload{}, errCancelled
	case StatusExpired:
		return Upload{}, errExpired
	case StatusFailed:
		return Upload{}, errFailed
	default:
		return Upload{}, errIncomplete
	}
}

// Complete verifies the stored object against what was declared and records the
// upload as ready for processing. It is idempotent.
func (s *Service) Complete(ctx context.Context, id, userID string) (Upload, error) {
	if err := s.expireStale(ctx, userID); err != nil {
		return Upload{}, err
	}
	up, err := s.load(ctx, id, userID)
	if err != nil {
		return Upload{}, err
	}
	if up.Status != StatusPending {
		return completeOutcome(up)
	}

	// Ask the store, not the client: the browser's claim proves nothing.
	info, err := s.storage.StatObject(ctx, up.objectKey)
	switch {
	case errors.Is(err, integrations.ErrObjectNotFound):
		return Upload{}, errIncomplete // still pending; the client may finish uploading and retry
	case err != nil:
		return Upload{}, storageError(ctx, err)
	}

	if info.Size != up.SizeBytes || normalizeContentType(info.ContentType) != up.ContentType {
		key, changed, err := s.terminate(ctx, up.ID, StatusFailed, reasonMismatch, "failed")
		if err != nil {
			return Upload{}, err
		}
		if changed {
			s.discard(ctx, key)
			return Upload{}, errRejected
		}
		return s.settled(ctx, id, userID)
	}

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var videoID string
		e := tx.QueryRow(ctx,
			`UPDATE video_uploads
			 SET status = 'uploaded', verified_size_bytes = $2, etag = $3, completed_at = now(), updated_at = now()
			 WHERE id = $1 AND status = 'pending'
			 RETURNING video_id`,
			up.ID, info.Size, info.ETag).Scan(&videoID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil // lost a race; settled() below reports the winner's result
		}
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx,
			`UPDATE videos SET status = 'processing', source_object_key = $2, updated_at = now() WHERE id = $1`,
			videoID, up.objectKey)
		return e
	})
	if err != nil {
		return Upload{}, err
	}
	return s.settled(ctx, id, userID)
}

// settled reloads an upload after a conditional update and reports the result
// of a complete request, whoever won a race.
func (s *Service) settled(ctx context.Context, id, userID string) (Upload, error) {
	up, err := s.load(ctx, id, userID)
	if err != nil {
		return Upload{}, err
	}
	return completeOutcome(up)
}

// Get returns the caller's upload.
func (s *Service) Get(ctx context.Context, id, userID string) (Upload, error) {
	if err := s.expireStale(ctx, userID); err != nil {
		return Upload{}, err
	}
	return s.load(ctx, id, userID)
}

// Cancel abandons a pending upload and deletes any object already stored.
// Cancelling an already cancelled, failed or expired upload succeeds (no-op);
// a completed upload cannot be cancelled (delete the video instead).
func (s *Service) Cancel(ctx context.Context, id, userID string) error {
	if err := s.expireStale(ctx, userID); err != nil {
		return err
	}
	up, err := s.load(ctx, id, userID)
	if err != nil {
		return err
	}
	switch up.Status {
	case StatusUploaded:
		return errAlreadyCompleted
	case StatusPending:
	default:
		return nil
	}

	key, changed, err := s.terminate(ctx, up.ID, StatusCancelled, "", "draft")
	if err != nil {
		return err
	}
	if changed {
		s.discard(ctx, key)
		return nil
	}
	// Another request finished first. If it completed the upload, cancelling is no longer possible.
	latest, err := s.load(ctx, id, userID)
	if err != nil {
		return err
	}
	if latest.Status == StatusUploaded {
		return errAlreadyCompleted
	}
	return nil
}

// List returns the caller's uploads, newest first, optionally filtered by status.
func (s *Service) List(ctx context.Context, userID, status string, limit, offset int) ([]Upload, error) {
	if err := s.expireStale(ctx, userID); err != nil {
		return nil, err
	}
	query := `SELECT ` + uploadColumns + ` FROM video_uploads WHERE user_id = $1`
	args := []any{userID}
	if status != "" {
		args = append(args, status)
		query += fmt.Sprintf(` AND status = $%d`, len(args))
	}
	args = append(args, limit, offset)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Upload{}
	for rows.Next() {
		u, _, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
