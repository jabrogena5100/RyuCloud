// Package jobs stores video-processing jobs in PostgreSQL.
//
// PostgreSQL is the source of truth for every job: its state, attempt count, retry
// schedule and which worker currently owns it (a time-limited lease). The queue only
// tells workers "look at job X", so a lost or duplicated message cannot corrupt a job.
//
// State machine (processing_jobs.state):
//
//	queued ──claim──► processing ──success──► succeeded
//	  ▲                  │  │
//	  │ release          │  └─permanent error / attempts exhausted──► failed
//	  └──────────────────┤
//	retry ◄──transient error (after backoff)
//	  └──claim──► processing
//
// Every transition is a single conditional UPDATE (or a short transaction that also
// updates the video), so concurrent workers, retries and sweeps cannot double-apply
// or overwrite each other: whoever's WHERE clause matches wins, everyone else sees
// zero rows and backs off.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job states.
const (
	StateQueued     = "queued"
	StateProcessing = "processing"
	StateRetry      = "retry"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
)

// ErrLeaseLost means the caller no longer owns the job (its lease expired and another
// worker took over, or the job was finished). The caller must stop and discard its work.
var ErrLeaseLost = errors.New("job lease lost")

// Job is a claimed job.
type Job struct {
	ID          string
	VideoID     string
	UploadID    string
	SourceKey   string
	Attempts    int // including the current one
	MaxAttempts int
	WorkerID    string
}

// Snapshot is a job's persisted state, for status displays and tests.
type Snapshot struct {
	ID          string
	VideoID     string
	State       string
	Stage       string
	Progress    int
	Attempts    int
	MaxAttempts int
	NextRunAt   time.Time
	LockedBy    *string
	LastError   *string
	FinishedAt  *time.Time
}

// Rendition describes one generated HLS rendition.
type Rendition struct {
	Name         string
	Width        int
	Height       int
	BandwidthBPS int64
	SegmentCount int
	SizeBytes    int64
	PlaylistKey  string
}

// Result is what a successful run produced.
type Result struct {
	DurationSeconds int
	ManifestKey     string
	ThumbnailKey    string
	Renditions      []Rendition
}

// Store reads and writes processing jobs.
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
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

// Create inserts the job for a video inside the caller's transaction. It reports
// created=false when the video already has a job (repeat completion), in which case
// nothing changes.
func Create(ctx context.Context, tx pgx.Tx, videoID, uploadID, sourceKey string, maxAttempts int) (jobID string, created bool, err error) {
	err = tx.QueryRow(ctx,
		`INSERT INTO processing_jobs (video_id, upload_id, source_object_key, max_attempts)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (video_id) DO NOTHING
		 RETURNING id`,
		videoID, uploadID, sourceKey, maxAttempts).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return jobID, true, nil
}

// MarkPublished records that the job's ID was sent to the queue, so the sweeper does
// not send it again right away.
func (s *Store) MarkPublished(ctx context.Context, jobID string) error {
	_, err := s.db.Exec(ctx, `UPDATE processing_jobs SET last_published_at = now() WHERE id = $1`, jobID)
	return err
}

// Claim takes ownership of a job if it is due (queued/retry whose time has come, or a
// processing job whose lease expired) and has attempts left. ok=false means there is
// nothing to do: it is finished, not due yet, owned by a live worker, or exhausted.
func (s *Store) Claim(ctx context.Context, jobID, workerID string, lease time.Duration) (job Job, ok bool, err error) {
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx,
			`UPDATE processing_jobs
			 SET state = 'processing', stage = 'starting', progress = 0, attempts = attempts + 1,
			     locked_by = $2, lease_expires_at = now() + make_interval(secs => $3),
			     last_published_at = NULL, started_at = COALESCE(started_at, now()), updated_at = now()
			 WHERE id = $1 AND attempts < max_attempts
			   AND ((state IN ('queued', 'retry') AND next_run_at <= now())
			        OR (state = 'processing' AND lease_expires_at < now()))
			 RETURNING id, video_id, upload_id, source_object_key, attempts, max_attempts`,
			jobID, workerID, lease.Seconds(),
		).Scan(&job.ID, &job.VideoID, &job.UploadID, &job.SourceKey, &job.Attempts, &job.MaxAttempts)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		ok = true
		_, e = tx.Exec(ctx,
			`UPDATE videos SET status = 'processing', processing_error = NULL, updated_at = now()
			 WHERE id = $1 AND status IN ('queued', 'processing')`, job.VideoID)
		return e
	})
	if err != nil {
		return Job{}, false, err
	}
	job.WorkerID = workerID
	return job, ok, nil
}

// Heartbeat extends the lease. It returns ErrLeaseLost if the caller no longer owns the job.
func (s *Store) Heartbeat(ctx context.Context, job Job, lease time.Duration) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE processing_jobs SET lease_expires_at = now() + make_interval(secs => $3), updated_at = now()
		 WHERE id = $1 AND locked_by = $2 AND state = 'processing'`,
		job.ID, job.WorkerID, lease.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// SetProgress records the current stage and percentage. It never moves progress backwards
// within an attempt and is ignored (ErrLeaseLost) if the caller lost the job.
func (s *Store) SetProgress(ctx context.Context, job Job, stage string, percent int) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 99 { // 100 is reserved for "persisted as ready"
		percent = 99
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE processing_jobs SET stage = $3, progress = GREATEST(progress, $4::smallint), updated_at = now()
		 WHERE id = $1 AND locked_by = $2 AND state = 'processing'`,
		job.ID, job.WorkerID, stage, percent)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Succeed persists a successful run: the job, the video and its renditions change
// together in one transaction. Only after this returns nil may the queue message be
// acknowledged. Reprocessing replaces the renditions, so the result is idempotent.
func (s *Store) Succeed(ctx context.Context, job Job, res Result) error {
	var thumbnail any // a video without a thumbnail is still playable
	if res.ThumbnailKey != "" {
		thumbnail = res.ThumbnailKey
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE processing_jobs
			 SET state = 'succeeded', stage = 'done', progress = 100, locked_by = NULL, lease_expires_at = NULL,
			     last_error = NULL, finished_at = now(), updated_at = now()
			 WHERE id = $1 AND locked_by = $2 AND state = 'processing'`,
			job.ID, job.WorkerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		if _, err := tx.Exec(ctx,
			`UPDATE videos
			 SET status = 'ready', processing_error = NULL, hls_manifest_key = $2, thumbnail_object_key = $3,
			     duration_seconds = $4, published_at = COALESCE(published_at, now()), updated_at = now()
			 WHERE id = $1`,
			job.VideoID, res.ManifestKey, thumbnail, res.DurationSeconds); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM video_renditions WHERE video_id = $1`, job.VideoID); err != nil {
			return err
		}
		for _, r := range res.Renditions {
			if _, err := tx.Exec(ctx,
				`INSERT INTO video_renditions (video_id, name, width, height, bandwidth_bps, segment_count, size_bytes, playlist_key)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				job.VideoID, r.Name, r.Width, r.Height, r.BandwidthBPS, r.SegmentCount, r.SizeBytes, r.PlaylistKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// Retry schedules another attempt after delay: the job goes back to "retry" and the
// video to "queued". The queue is not told; the sweeper re-sends the job when it is due.
func (s *Store) Retry(ctx context.Context, job Job, reason string, delay time.Duration) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE processing_jobs
			 SET state = 'retry', stage = 'waiting to retry', next_run_at = now() + make_interval(secs => $3),
			     last_error = $4, locked_by = NULL, lease_expires_at = NULL, last_published_at = NULL, updated_at = now()
			 WHERE id = $1 AND locked_by = $2 AND state = 'processing'`,
			job.ID, job.WorkerID, delay.Seconds(), truncate(reason, 1000))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		_, err = tx.Exec(ctx, `UPDATE videos SET status = 'queued', updated_at = now() WHERE id = $1 AND status = 'processing'`, job.VideoID)
		return err
	})
}

// Fail ends the job for good. userMessage is shown to the video's owner; detail is for operators.
func (s *Store) Fail(ctx context.Context, job Job, userMessage, detail string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE processing_jobs
			 SET state = 'failed', stage = 'failed', last_error = $3, locked_by = NULL, lease_expires_at = NULL,
			     finished_at = now(), updated_at = now()
			 WHERE id = $1 AND locked_by = $2 AND state = 'processing'`,
			job.ID, job.WorkerID, truncate(detail, 1000))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		_, err = tx.Exec(ctx,
			`UPDATE videos SET status = 'failed', processing_error = $2, updated_at = now() WHERE id = $1`,
			job.VideoID, truncate(userMessage, 500))
		return err
	})
}

// Release hands a job back without counting the attempt (used on shutdown, when the
// work was interrupted through no fault of the job).
func (s *Store) Release(ctx context.Context, job Job) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE processing_jobs
			 SET state = 'queued', stage = 'queued', attempts = GREATEST(attempts - 1, 0), next_run_at = now(),
			     locked_by = NULL, lease_expires_at = NULL, last_published_at = NULL, updated_at = now()
			 WHERE id = $1 AND locked_by = $2 AND state = 'processing'`,
			job.ID, job.WorkerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		_, err = tx.Exec(ctx, `UPDATE videos SET status = 'queued', updated_at = now() WHERE id = $1 AND status = 'processing'`, job.VideoID)
		return err
	})
}

// FailExhausted fails jobs whose worker died on their last allowed attempt (lease
// expired with no attempts left). It returns how many it failed.
func (s *Store) FailExhausted(ctx context.Context, userMessage string) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE processing_jobs
			 SET state = 'failed', stage = 'failed', last_error = 'worker lost the job on its final attempt',
			     locked_by = NULL, lease_expires_at = NULL, finished_at = now(), updated_at = now()
			 WHERE state = 'processing' AND lease_expires_at < now() AND attempts >= max_attempts
			 RETURNING video_id`)
		if err != nil {
			return err
		}
		var videoIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			videoIDs = append(videoIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range videoIDs {
			if _, err := tx.Exec(ctx,
				`UPDATE videos SET status = 'failed', processing_error = $2, updated_at = now() WHERE id = $1`,
				id, truncate(userMessage, 500)); err != nil {
				return err
			}
		}
		n = len(videoIDs)
		return nil
	})
	return n, err
}

// DueForPublish returns jobs that should be (re)sent to the queue: due queued/retry
// jobs and processing jobs whose lease expired (their worker died), skipping any sent
// within the last minAge. It stamps them as published, so concurrent sweepers do not
// pick the same jobs (FOR UPDATE SKIP LOCKED).
func (s *Store) DueForPublish(ctx context.Context, minAge time.Duration, limit int) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`UPDATE processing_jobs SET last_published_at = now()
		 WHERE id IN (
		   SELECT id FROM processing_jobs
		   WHERE ((state IN ('queued', 'retry') AND next_run_at <= now())
		          OR (state = 'processing' AND lease_expires_at < now() AND attempts < max_attempts))
		     AND (last_published_at IS NULL OR last_published_at < now() - make_interval(secs => $1))
		   ORDER BY next_run_at
		   LIMIT $2
		   FOR UPDATE SKIP LOCKED)
		 RETURNING id`, minAge.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Get returns a job's persisted state by video ID.
func (s *Store) Get(ctx context.Context, videoID string) (Snapshot, error) {
	var sn Snapshot
	err := s.db.QueryRow(ctx,
		`SELECT id, video_id, state, stage, progress, attempts, max_attempts, next_run_at, locked_by, last_error, finished_at
		 FROM processing_jobs WHERE video_id = $1`, videoID,
	).Scan(&sn.ID, &sn.VideoID, &sn.State, &sn.Stage, &sn.Progress, &sn.Attempts, &sn.MaxAttempts,
		&sn.NextRunAt, &sn.LockedBy, &sn.LastError, &sn.FinishedAt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load job for video %s: %w", videoID, err)
	}
	return sn, nil
}

// truncate cuts s to at most max characters without splitting a multi-byte character.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
