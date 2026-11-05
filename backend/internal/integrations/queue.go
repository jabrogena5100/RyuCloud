package integrations

import (
	"context"
	"sync"
	"time"
)

// Delivery is one message handed to a worker. The message carries only a job ID:
// everything else (state, attempts, lease, retry schedule) lives in PostgreSQL, so
// a lost, duplicated or late message can never corrupt a job.
type Delivery struct {
	JobID string
	// receipt identifies the in-flight copy (for SQS this would be the receipt handle).
	receipt string
}

// Queue delivers job IDs from the API to workers with at-least-once semantics.
//
//	Publish  - make the job available (duplicates are harmless)
//	Receive  - take one message, or (nil, nil) after wait with nothing available
//	Ack      - drop the in-flight message; call it only AFTER the outcome is persisted
//	Requeue  - make the job available again and forget any in-flight copy (crash recovery)
//
// Redis implements it with two lists; an SQS implementation maps Publish to
// SendMessage, Receive to ReceiveMessage, Ack to DeleteMessage and Requeue to
// SendMessage. Nothing outside this package depends on Redis.
type Queue interface {
	Publish(ctx context.Context, jobID string) error
	Receive(ctx context.Context, wait time.Duration) (*Delivery, error)
	Ack(ctx context.Context, d *Delivery) error
	Requeue(ctx context.Context, jobID string) error
}

// DisabledQueue fails loudly instead of silently dropping jobs. The API treats a
// failed Publish as non-fatal because the job row is already durable and a worker's
// sweeper will find it.
type DisabledQueue struct{}

func (DisabledQueue) Publish(context.Context, string) error { return ErrNotConfigured }
func (DisabledQueue) Receive(context.Context, time.Duration) (*Delivery, error) {
	return nil, ErrNotConfigured
}
func (DisabledQueue) Ack(context.Context, *Delivery) error  { return ErrNotConfigured }
func (DisabledQueue) Requeue(context.Context, string) error { return ErrNotConfigured }

// MemoryQueue is an in-process Queue with the same semantics as the Redis one.
// For tests and single-process experiments only: it is not durable.
type MemoryQueue struct {
	mu       sync.Mutex
	ready    []string
	inflight []string
	// Published counts every Publish/Requeue call, for assertions.
	published []string
	failNext  error
}

func NewMemoryQueue() *MemoryQueue { return &MemoryQueue{} }

// FailWith makes Publish/Requeue return err until cleared with nil.
func (q *MemoryQueue) FailWith(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failNext = err
}

func (q *MemoryQueue) Publish(_ context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failNext != nil {
		return q.failNext
	}
	q.ready = append(q.ready, jobID)
	q.published = append(q.published, jobID)
	return nil
}

func (q *MemoryQueue) Receive(ctx context.Context, wait time.Duration) (*Delivery, error) {
	deadline := time.Now().Add(wait)
	for {
		q.mu.Lock()
		if len(q.ready) > 0 {
			id := q.ready[0]
			q.ready = q.ready[1:]
			q.inflight = append(q.inflight, id)
			q.mu.Unlock()
			return &Delivery{JobID: id, receipt: id}, nil
		}
		q.mu.Unlock()
		if !time.Now().Before(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (q *MemoryQueue) Ack(_ context.Context, d *Delivery) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inflight = removeFirst(q.inflight, d.receipt)
	return nil
}

func (q *MemoryQueue) Requeue(_ context.Context, jobID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failNext != nil {
		return q.failNext
	}
	q.inflight = removeAll(q.inflight, jobID)
	q.ready = append(removeAll(q.ready, jobID), jobID)
	q.published = append(q.published, jobID)
	return nil
}

// Published returns every job ID passed to Publish or Requeue, in order.
func (q *MemoryQueue) Published() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.published...)
}

// Len returns the number of waiting and in-flight messages.
func (q *MemoryQueue) Len() (ready, inflight int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ready), len(q.inflight)
}

func removeFirst(list []string, v string) []string {
	for i, x := range list {
		if x == v {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func removeAll(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
