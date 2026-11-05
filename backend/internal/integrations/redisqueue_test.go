package integrations

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// RedisQueue is exercised against miniredis, an in-process Redis implementation, so
// these tests need no running server. The end-to-end script covers real Redis.

func newTestRedisQueue(t *testing.T) (*RedisQueue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	q, err := NewRedisQueue(context.Background(), "redis://"+mr.Addr()+"/0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q, mr
}

func TestRedisQueuePublishReceiveAck(t *testing.T) {
	ctx := context.Background()
	q, _ := newTestRedisQueue(t)

	if d, err := q.Receive(ctx, 50*time.Millisecond); d != nil || err != nil {
		t.Fatalf("empty queue: got %v, %v", d, err)
	}
	if err := q.Publish(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	d, err := q.Receive(ctx, time.Second)
	if err != nil || d == nil || d.JobID != "job-1" {
		t.Fatalf("Receive = %v, %v", d, err)
	}
	if ready, inflight, _ := q.Depth(ctx); ready != 0 || inflight != 1 {
		t.Fatalf("after receive: ready=%d inflight=%d", ready, inflight)
	}
	if err := q.Ack(ctx, d); err != nil {
		t.Fatal(err)
	}
	if ready, inflight, _ := q.Depth(ctx); ready != 0 || inflight != 0 {
		t.Fatalf("after ack: ready=%d inflight=%d", ready, inflight)
	}
}

func TestRedisQueueIsFIFO(t *testing.T) {
	ctx := context.Background()
	q, _ := newTestRedisQueue(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := q.Publish(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		d, err := q.Receive(ctx, time.Second)
		if err != nil || d == nil || d.JobID != want {
			t.Fatalf("Receive = %v, %v; want %s", d, err, want)
		}
	}
}

// A worker that dies after Receive never acks. Requeue must recover the job and drop
// the stale in-flight copy so the queue does not grow.
func TestRedisQueueRequeueRecoversAbandonedMessages(t *testing.T) {
	ctx := context.Background()
	q, _ := newTestRedisQueue(t)
	_ = q.Publish(ctx, "job-1")
	if d, _ := q.Receive(ctx, time.Second); d == nil {
		t.Fatal("expected a delivery")
	}
	// ... the worker crashes here; the message sits in "inflight" ...

	if err := q.Requeue(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	if ready, inflight, _ := q.Depth(ctx); ready != 1 || inflight != 0 {
		t.Fatalf("after requeue: ready=%d inflight=%d", ready, inflight)
	}
	d, err := q.Receive(ctx, time.Second)
	if err != nil || d == nil || d.JobID != "job-1" {
		t.Fatalf("recovered Receive = %v, %v", d, err)
	}
}

func TestRedisQueueRequeueDeduplicates(t *testing.T) {
	ctx := context.Background()
	q, _ := newTestRedisQueue(t)
	_ = q.Publish(ctx, "job-1")
	_ = q.Requeue(ctx, "job-1")
	_ = q.Requeue(ctx, "job-1")
	if ready, _, _ := q.Depth(ctx); ready != 1 {
		t.Fatalf("ready = %d, want 1", ready)
	}
}

func TestRedisQueueReceiveHonoursContext(t *testing.T) {
	q, _ := newTestRedisQueue(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := q.Receive(ctx, time.Minute); err == nil {
		t.Fatal("expected the context error")
	}
}

func TestRedisQueueSurvivesARedisRestart(t *testing.T) {
	ctx := context.Background()
	q, mr := newTestRedisQueue(t)
	mr.Close()
	if err := q.Publish(ctx, "x"); err == nil {
		t.Fatal("Publish should fail while Redis is down")
	}
	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := q.Publish(ctx, "x"); err != nil {
		t.Fatalf("Publish should work again after Redis returns: %v", err)
	}
}

func TestNewRedisQueueRejectsBadURLAndUnreachableServer(t *testing.T) {
	if _, err := NewRedisQueue(context.Background(), "not a url"); err == nil {
		t.Error("expected a parse error")
	}
	if _, err := NewRedisQueue(context.Background(), "redis://127.0.0.1:1/0"); err == nil {
		t.Error("expected a connection error")
	}
}
