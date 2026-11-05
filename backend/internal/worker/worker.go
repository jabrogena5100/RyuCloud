// Package worker is the asynchronous video-processing engine run by cmd/worker.
//
// Delivery is at-least-once and every step is safe to repeat:
//
//   - A message on the queue only names a job. The worker claims the job in
//     PostgreSQL (a conditional UPDATE that also takes a time-limited lease), so a
//     duplicate or stale message finds nothing to claim and is dropped.
//   - While it works the worker renews the lease. If the worker dies, the lease
//     expires and the sweeper re-sends the job; another worker claims it.
//   - The result (video READY + renditions) is persisted in one transaction, and the
//     queue message is acknowledged only after that commit succeeds. If anything
//     fails before it, the message stays unacknowledged and lease expiry recovers it.
//   - Output keys depend only on the video ID, so reprocessing overwrites rather
//     than duplicates.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/jobs"
	"github.com/jabrogena5100/RyuCloud/backend/internal/transcode"
)

const (
	// receiveWait is how long one Receive blocks, which bounds shutdown latency.
	receiveWait = 2 * time.Second
	// persistTimeout bounds writing an outcome to PostgreSQL.
	persistTimeout = 30 * time.Second
	// staleTempAge is how old a leftover job directory must be before startup removes it.
	staleTempAge = time.Hour
	// sweepBatch caps how many due jobs one sweep re-sends.
	sweepBatch = 100

	msgExhausted = "Processing failed after several attempts. Please upload the video again."
)

// Deps are the worker's collaborators; tests replace Storage, Queue and Media.
type Deps struct {
	Store    *jobs.Store
	Storage  integrations.ObjectStorage
	Queue    integrations.Queue
	Media    transcode.Media
	Config   config.WorkerConfig
	Logger   *slog.Logger
	WorkerID string // defaults to hostname plus random suffix
	// Jitter returns a value in [0,1) used to spread retry delays. Defaults to crypto/rand.
	Jitter func() float64
}

// Worker processes jobs.
type Worker struct {
	store   *jobs.Store
	storage integrations.ObjectStorage
	queue   integrations.Queue
	media   transcode.Media
	cfg     config.WorkerConfig
	log     *slog.Logger
	id      string
	jitter  func() float64
}

func New(d Deps) *Worker {
	w := &Worker{
		store: d.Store, storage: d.Storage, queue: d.Queue, media: d.Media,
		cfg: d.Config, log: d.Logger, id: d.WorkerID, jitter: d.Jitter,
	}
	if w.log == nil {
		w.log = slog.Default()
	}
	if w.id == "" {
		host, _ := os.Hostname()
		w.id = host + "-" + randomHex(3)
	}
	if w.jitter == nil {
		w.jitter = func() float64 {
			b := make([]byte, 2)
			_, _ = rand.Read(b)
			return float64(int(b[0])<<8|int(b[1])) / 65536
		}
	}
	return w
}

// ID is this worker's identity as recorded in job leases.
func (w *Worker) ID() string { return w.id }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Backoff returns the delay before retry number attempt (1-based): base, 2*base, 4*base
// ... capped at max, spread by +/-20% using jitter in [0,1) so retries do not synchronise.
func Backoff(attempt int, base, max time.Duration, jitter float64) time.Duration {
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*jitter))
	if d > max {
		d = max
	}
	return d
}

// Run consumes the queue until ctx is cancelled, then shuts down gracefully: it stops
// taking new jobs, lets running ones finish for up to ShutdownGrace, and finally
// releases any still running back to the queue (without consuming an attempt).
func (w *Worker) Run(ctx context.Context) error {
	w.cleanStaleTemp()
	w.log.Info("worker started", "worker_id", w.id, "concurrency", w.cfg.Concurrency,
		"max_attempts", w.cfg.MaxAttempts, "lease", w.cfg.Lease.String())

	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()

	var running sync.WaitGroup
	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		w.sweepLoop(ctx)
	}()

	slots := make(chan struct{}, w.cfg.Concurrency)
loop:
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		d, err := w.queue.Receive(ctx, receiveWait)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				break loop
			}
			w.log.Error("receive failed", "error", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				break loop
			}
			continue
		}
		if d == nil {
			<-slots
			continue
		}
		running.Add(1)
		go func() {
			defer running.Done()
			defer func() { <-slots }()
			w.handle(jobCtx, d)
		}()
	}

	w.log.Info("shutting down: no new jobs will be taken", "grace", w.cfg.ShutdownGrace.String())
	<-sweeperDone
	finished := make(chan struct{})
	go func() {
		running.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(w.cfg.ShutdownGrace):
		w.log.Warn("grace period over: interrupting running jobs and handing them back")
		cancelJobs()
		<-finished
	}
	w.log.Info("worker stopped")
	return nil
}

// handle processes one delivery and acknowledges it only once the outcome is persisted.
func (w *Worker) handle(ctx context.Context, d *integrations.Delivery) {
	if err := w.ProcessJob(ctx, d.JobID); err != nil {
		// The outcome is not safely stored. Leave the message unacknowledged: the job's
		// lease will expire and the sweeper re-sends it.
		w.log.Error("job outcome not persisted; leaving it for lease recovery", "job_id", d.JobID, "error", err)
		return
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.queue.Ack(ackCtx, d); err != nil {
		w.log.Warn("acknowledge failed; the message will be dropped on the next requeue", "job_id", d.JobID, "error", err)
	}
}

func (w *Worker) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		if err := w.Sweep(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("sweep failed", "error", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// Sweep is the recovery pass. It fails jobs whose worker died on the final allowed
// attempt, and re-sends to the queue every job that should be running but has no live
// owner: retries whose backoff has elapsed, jobs whose publish was lost, and jobs
// whose worker crashed (expired lease).
func (w *Worker) Sweep(ctx context.Context) error {
	failed, err := w.store.FailExhausted(ctx, msgExhausted)
	if err != nil {
		return fmt.Errorf("fail exhausted jobs: %w", err)
	}
	if failed > 0 {
		w.log.Warn("failed jobs whose worker died on the last attempt", "count", failed)
	}

	ids, err := w.store.DueForPublish(ctx, w.cfg.RepublishAge, sweepBatch)
	if err != nil {
		return fmt.Errorf("find due jobs: %w", err)
	}
	for _, id := range ids {
		if err := w.queue.Requeue(ctx, id); err != nil {
			w.log.Error("requeue failed", "job_id", id, "error", err)
			continue
		}
		w.log.Info("job re-sent to queue", "job_id", id)
	}
	return nil
}

// cleanStaleTemp removes job directories left behind by a worker that was killed.
func (w *Worker) cleanStaleTemp() {
	entries, err := os.ReadDir(w.cfg.TempDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "job-") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < staleTempAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(w.cfg.TempDir, e.Name())); err == nil {
			w.log.Info("removed stale temp directory", "dir", e.Name())
		}
	}
}

// permanent marks a failure that retrying cannot fix. user is shown to the video's owner.
type permanent struct {
	user string
	err  error
}

func (p *permanent) Error() string { return p.err.Error() }
func (p *permanent) Unwrap() error { return p.err }

// ProcessJob claims and processes one job, then records the outcome. A nil return means
// the outcome is safely stored (or there was nothing to do), so the message may be
// acknowledged; an error means it is not.
func (w *Worker) ProcessJob(ctx context.Context, jobID string) error {
	if !httpx.IsUUID(jobID) {
		w.log.Warn("dropping message with a malformed job id")
		return nil
	}
	job, ok, err := w.store.Claim(ctx, jobID, w.id, w.cfg.Lease)
	if err != nil {
		return fmt.Errorf("claim job: %w", err)
	}
	if !ok {
		w.log.Debug("nothing to claim (finished, not due, or owned by another worker)", "job_id", jobID)
		return nil
	}
	log := w.log.With("job_id", job.ID, "video_id", job.VideoID, "attempt", job.Attempts, "max_attempts", job.MaxAttempts)
	log.Info("job claimed")
	started := time.Now()

	// workCtx is cancelled with ErrLeaseLost if the lease is lost, and bounded by the job timeout.
	workCtx, cancelWork := context.WithCancelCause(ctx)
	defer cancelWork(nil)
	runCtx, cancelTimeout := context.WithTimeout(workCtx, w.cfg.Timeout)
	defer cancelTimeout()

	stopHeartbeat := w.startHeartbeat(runCtx, job, cancelWork)
	result, runErr := w.run(runCtx, job)
	stopHeartbeat()

	// Outcomes are written with a context that survives cancellation of the work itself.
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancelPersist()

	switch {
	case runErr == nil:
		err = w.store.Succeed(persistCtx, job, result)
		if err == nil {
			log.Info("job succeeded", "duration", time.Since(started).Round(time.Millisecond).String(), "renditions", len(result.Renditions))
		}
	case errors.Is(context.Cause(workCtx), jobs.ErrLeaseLost):
		log.Warn("lease lost; abandoning this attempt")
		return nil
	case ctx.Err() != nil:
		log.Warn("interrupted by shutdown; handing the job back")
		err = w.store.Release(persistCtx, job)
	default:
		err = w.recordFailure(persistCtx, log, job, runErr)
	}
	if errors.Is(err, jobs.ErrLeaseLost) {
		log.Warn("lease lost before the outcome could be recorded; another worker owns the job now")
		return nil
	}
	return err
}

// recordFailure decides between failing the job for good and scheduling a retry.
func (w *Worker) recordFailure(ctx context.Context, log *slog.Logger, job jobs.Job, runErr error) error {
	var perm *permanent
	switch {
	case errors.As(runErr, &perm):
		log.Error("job failed permanently", "error", runErr)
		return w.store.Fail(ctx, job, perm.user, runErr.Error())
	case job.Attempts >= job.MaxAttempts:
		log.Error("job failed: attempts exhausted", "error", runErr)
		return w.store.Fail(ctx, job, msgExhausted, "attempts exhausted: "+runErr.Error())
	}
	delay := Backoff(job.Attempts, w.cfg.RetryBase, w.cfg.RetryMax, w.jitter())
	log.Warn("job failed; will retry", "error", runErr, "retry_in", delay.Round(time.Second).String())
	return w.store.Retry(ctx, job, runErr.Error(), delay)
}

// startHeartbeat renews the lease until the returned stop function is called. If the
// lease is lost it cancels the work with jobs.ErrLeaseLost.
func (w *Worker) startHeartbeat(ctx context.Context, job jobs.Job, cancel context.CancelCauseFunc) (stop func()) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	interval := w.cfg.Lease / 3
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := w.store.Heartbeat(context.WithoutCancel(ctx), job, w.cfg.Lease)
				switch {
				case errors.Is(err, jobs.ErrLeaseLost):
					cancel(jobs.ErrLeaseLost)
					return
				case err != nil:
					w.log.Warn("heartbeat failed", "job_id", job.ID, "error", err)
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}
