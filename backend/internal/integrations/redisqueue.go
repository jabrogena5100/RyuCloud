package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisReadyKey    = "ryucloud:jobs:ready"
	redisInflightKey = "ryucloud:jobs:inflight"
	// redisPollInterval is how often Receive re-checks an empty queue. Polling keeps
	// Receive interruptible and uses only commands every Redis-compatible server has.
	redisPollInterval = 250 * time.Millisecond
)

// RedisQueue is the local-development Queue.
//
//	Publish:  LPUSH ready <id>
//	Receive:  RPOPLPUSH ready inflight   (atomic move: a crash cannot lose a message)
//	Ack:      LREM inflight 1 <id>
//	Requeue:  LREM inflight/ready <id>, then LPUSH ready <id>   (also de-duplicates)
//
// Messages left in "inflight" by a crashed worker are not retried by timing out; the
// job's lease in PostgreSQL expires and the sweeper calls Requeue, which removes the
// stale copy. Redis is therefore only a delivery channel, not the source of truth.
type RedisQueue struct {
	rdb *redis.Client
}

// NewRedisQueue connects to Redis (URL like redis://host:6379/0) and checks it responds.
func NewRedisQueue(ctx context.Context, url string) (*RedisQueue, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("connect to redis: %w", err)
	}
	return &RedisQueue{rdb: rdb}, nil
}

// Close releases the connection pool.
func (q *RedisQueue) Close() error { return q.rdb.Close() }

func (q *RedisQueue) Publish(ctx context.Context, jobID string) error {
	return q.rdb.LPush(ctx, redisReadyKey, jobID).Err()
}

func (q *RedisQueue) Receive(ctx context.Context, wait time.Duration) (*Delivery, error) {
	deadline := time.Now().Add(wait)
	for {
		id, err := q.rdb.RPopLPush(ctx, redisReadyKey, redisInflightKey).Result()
		switch {
		case err == nil:
			return &Delivery{JobID: id, receipt: id}, nil
		case !errors.Is(err, redis.Nil):
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(redisPollInterval):
		}
	}
}

func (q *RedisQueue) Ack(ctx context.Context, d *Delivery) error {
	return q.rdb.LRem(ctx, redisInflightKey, 1, d.receipt).Err()
}

func (q *RedisQueue) Requeue(ctx context.Context, jobID string) error {
	pipe := q.rdb.TxPipeline()
	pipe.LRem(ctx, redisInflightKey, 0, jobID)
	pipe.LRem(ctx, redisReadyKey, 0, jobID)
	pipe.LPush(ctx, redisReadyKey, jobID)
	_, err := pipe.Exec(ctx)
	return err
}

// Depth reports how many messages are waiting and in flight (for logs and tests).
func (q *RedisQueue) Depth(ctx context.Context) (ready, inflight int64, err error) {
	if ready, err = q.rdb.LLen(ctx, redisReadyKey).Result(); err != nil {
		return 0, 0, err
	}
	inflight, err = q.rdb.LLen(ctx, redisInflightKey).Result()
	return ready, inflight, err
}
