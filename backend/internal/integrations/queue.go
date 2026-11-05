package integrations

import (
	"context"
	"sync"
)

// TranscodeJob asks the video worker to process an uploaded source file.
type TranscodeJob struct {
	VideoID         string
	SourceObjectKey string
}

// JobQueue hands work to the separate video worker process.
type JobQueue interface {
	EnqueueTranscode(ctx context.Context, job TranscodeJob) error
}

// DisabledQueue fails loudly instead of silently dropping jobs.
type DisabledQueue struct{}

func (DisabledQueue) EnqueueTranscode(context.Context, TranscodeJob) error { return ErrNotConfigured }

// MemoryQueue records jobs in-process. For tests and local experiments only: not durable.
type MemoryQueue struct {
	mu   sync.Mutex
	jobs []TranscodeJob
}

func (q *MemoryQueue) EnqueueTranscode(_ context.Context, job TranscodeJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	return nil
}

// Jobs returns a copy of the recorded jobs.
func (q *MemoryQueue) Jobs() []TranscodeJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]TranscodeJob(nil), q.jobs...)
}

// NewQueue returns the queue for a QUEUE_BACKEND value ("memory" or "disabled").
func NewQueue(backend string) JobQueue {
	if backend == "memory" {
		return &MemoryQueue{}
	}
	return DisabledQueue{}
}
