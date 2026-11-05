package integrations

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

func TestSourceUploadKey(t *testing.T) {
	if got := SourceUploadKey("vid", "up"); got != "uploads/vid/up/source" {
		t.Fatalf("SourceUploadKey = %q", got)
	}
	if SourceUploadKey("vid", "up1") == SourceUploadKey("vid", "up2") {
		t.Fatal("different upload attempts must get different keys")
	}
}

func TestDisabledStorageFailsLoudly(t *testing.T) {
	ctx := context.Background()
	s, err := NewStorage(ctx, config.Config{StorageBackend: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PresignUpload(ctx, PresignRequest{ObjectKey: "k", ContentType: "video/mp4", SizeBytes: 1}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("PresignUpload error = %v", err)
	}
	if _, err := s.StatObject(ctx, "k"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("StatObject error = %v", err)
	}
	if err := s.DeleteObject(ctx, "k"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("DeleteObject error = %v", err)
	}
}

func TestNewStorageRejectsUnknownBackend(t *testing.T) {
	if _, err := NewStorage(context.Background(), config.Config{StorageBackend: "gcs"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMemoryStorage(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStorage()

	if _, err := m.StatObject(ctx, "k"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("StatObject before Put = %v", err)
	}
	up, err := m.PresignUpload(ctx, PresignRequest{ObjectKey: "k", ContentType: "video/mp4", SizeBytes: 5, TTL: time.Minute})
	if err != nil || up.Method != "PUT" || up.Headers["Content-Type"] != "video/mp4" {
		t.Fatalf("PresignUpload = %+v, %v", up, err)
	}
	m.Put("k", "video/mp4", 5)
	if info, err := m.StatObject(ctx, "k"); err != nil || info.Size != 5 || info.ContentType != "video/mp4" {
		t.Fatalf("StatObject = %+v, %v", info, err)
	}
	if err := m.DeleteObject(ctx, "k"); err != nil || m.Has("k") {
		t.Fatalf("DeleteObject: err=%v has=%v", err, m.Has("k"))
	}
	if d := m.Deleted(); len(d) != 1 || d[0] != "k" {
		t.Fatalf("Deleted = %v", d)
	}
}

func TestMemoryStorageFailureInjection(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStorage()
	boom := errors.New("boom")
	for _, op := range []string{OpPresign, OpStat, OpDelete} {
		m.FailWith(op, boom)
	}
	if _, err := m.PresignUpload(ctx, PresignRequest{}); !errors.Is(err, boom) {
		t.Errorf("presign = %v", err)
	}
	if _, err := m.StatObject(ctx, "k"); !errors.Is(err, boom) {
		t.Errorf("stat = %v", err)
	}
	if err := m.DeleteObject(ctx, "k"); !errors.Is(err, boom) {
		t.Errorf("delete = %v", err)
	}
	m.FailWith(OpStat, nil)
	if _, err := m.StatObject(ctx, "k"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("stat after clearing the failure = %v", err)
	}
}

func TestMemoryQueueDeliversAcksAndRequeues(t *testing.T) {
	ctx := context.Background()
	q := NewMemoryQueue()

	if d, err := q.Receive(ctx, 20*time.Millisecond); d != nil || err != nil {
		t.Fatalf("empty queue: got %v, %v", d, err)
	}
	if err := q.Publish(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	d, err := q.Receive(ctx, time.Second)
	if err != nil || d == nil || d.JobID != "job-1" {
		t.Fatalf("Receive = %v, %v", d, err)
	}
	if ready, inflight := q.Len(); ready != 0 || inflight != 1 {
		t.Fatalf("after receive: ready=%d inflight=%d", ready, inflight)
	}

	// A crashed consumer never acks; requeue removes the stale in-flight copy.
	if err := q.Requeue(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	if ready, inflight := q.Len(); ready != 1 || inflight != 0 {
		t.Fatalf("after requeue: ready=%d inflight=%d", ready, inflight)
	}
	d, _ = q.Receive(ctx, time.Second)
	if err := q.Ack(ctx, d); err != nil {
		t.Fatal(err)
	}
	if ready, inflight := q.Len(); ready != 0 || inflight != 0 {
		t.Fatalf("after ack: ready=%d inflight=%d", ready, inflight)
	}
}

func TestMemoryQueueRequeueDeduplicates(t *testing.T) {
	ctx := context.Background()
	q := NewMemoryQueue()
	_ = q.Publish(ctx, "a")
	_ = q.Requeue(ctx, "a")
	_ = q.Requeue(ctx, "a")
	if ready, _ := q.Len(); ready != 1 {
		t.Fatalf("ready = %d, want 1", ready)
	}
}

func TestMemoryQueueReceiveHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewMemoryQueue().Receive(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDisabledQueueFailsLoudly(t *testing.T) {
	q := DisabledQueue{}
	if err := q.Publish(context.Background(), "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Publish error = %v", err)
	}
	if _, err := q.Receive(context.Background(), 0); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Receive error = %v", err)
	}
}

func TestNewQueueSelectsBackend(t *testing.T) {
	ctx := context.Background()
	if q, err := NewQueue(ctx, config.Config{QueueBackend: "memory"}); err != nil {
		t.Fatal(err)
	} else if _, ok := q.(*MemoryQueue); !ok {
		t.Fatalf("memory backend returned %T", q)
	}
	if q, err := NewQueue(ctx, config.Config{QueueBackend: "disabled"}); err != nil || q == nil {
		t.Fatalf("disabled backend: %v, %v", q, err)
	}
	if _, err := NewQueue(ctx, config.Config{QueueBackend: "sqs"}); err == nil {
		t.Fatal("expected an error for an unknown backend")
	}
}

func TestNullPlaybackReturnsNoURLs(t *testing.T) {
	key := "videos/x/hls/master.m3u8"
	p := NullPlayback{}
	if p.PlaybackURL("x", &key) != nil || p.ThumbnailURL("x", &key) != nil {
		t.Fatal("expected nil URLs")
	}
}

func TestAPIPlaybackBuildsAuthorizedMediaURLs(t *testing.T) {
	key := "videos/abc/hls/master.m3u8"
	p := NewPlaybackProvider("/api/v1")
	if got := p.PlaybackURL("abc", &key); got == nil || *got != "/api/v1/videos/abc/hls/master.m3u8" {
		t.Fatalf("PlaybackURL = %v", got)
	}
	if got := p.ThumbnailURL("abc", &key); got == nil || *got != "/api/v1/videos/abc/thumbnail" {
		t.Fatalf("ThumbnailURL = %v", got)
	}
	if p.PlaybackURL("abc", nil) != nil || p.ThumbnailURL("abc", nil) != nil {
		t.Fatal("videos that are not processed must have no URLs")
	}
}

func TestKeyLayout(t *testing.T) {
	if got := HLSMasterKey("v"); got != "videos/v/hls/master.m3u8" {
		t.Errorf("HLSMasterKey = %q", got)
	}
	if got := HLSKey("v", "360p/index.m3u8"); got != "videos/v/hls/360p/index.m3u8" {
		t.Errorf("HLSKey = %q", got)
	}
	if got := ThumbnailKey("v"); got != "videos/v/thumbnail.jpg" {
		t.Errorf("ThumbnailKey = %q", got)
	}
	if got := VideoPrefix("v"); got != "videos/v/" {
		t.Errorf("VideoPrefix = %q", got)
	}
}

func TestMemoryStorageObjectsAndPrefixes(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStorage()
	if err := m.PutObject(ctx, "videos/v/hls/master.m3u8", "application/vnd.apple.mpegurl", strings.NewReader("#EXTM3U"), 7); err != nil {
		t.Fatal(err)
	}
	m.PutData("videos/v/thumbnail.jpg", "image/jpeg", []byte{0xff, 0xd8})
	m.PutData("videos/other/thumbnail.jpg", "image/jpeg", []byte{1})

	obj, err := m.GetObject(ctx, "videos/v/hls/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	if string(body) != "#EXTM3U" || obj.ContentType != "application/vnd.apple.mpegurl" {
		t.Fatalf("GetObject = %q (%s)", body, obj.ContentType)
	}
	if _, err := m.GetObject(ctx, "missing"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing object error = %v", err)
	}

	n, err := m.DeletePrefix(ctx, VideoPrefix("v"))
	if err != nil || n != 2 {
		t.Fatalf("DeletePrefix = %d, %v", n, err)
	}
	if !m.Has("videos/other/thumbnail.jpg") || m.Has("videos/v/thumbnail.jpg") {
		t.Fatalf("keys after DeletePrefix: %v", m.Keys())
	}

	boom := errors.New("boom")
	m.FailWith(OpPut, boom)
	if err := m.PutObject(ctx, "k", "text/plain", strings.NewReader("x"), 1); !errors.Is(err, boom) {
		t.Fatalf("PutObject error = %v", err)
	}
	m.FailWith(OpGet, boom)
	if _, err := m.GetObject(ctx, "videos/other/thumbnail.jpg"); !errors.Is(err, boom) {
		t.Fatalf("GetObject error = %v", err)
	}
}
