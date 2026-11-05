package integrations

import (
	"context"
	"errors"
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

func TestMemoryQueueRecordsJobs(t *testing.T) {
	q := NewQueue("memory")
	job := TranscodeJob{VideoID: "v1", SourceObjectKey: "uploads/v1/source"}
	if err := q.EnqueueTranscode(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	mem, ok := q.(*MemoryQueue)
	if !ok {
		t.Fatalf("NewQueue(memory) returned %T", q)
	}
	if jobs := mem.Jobs(); len(jobs) != 1 || jobs[0] != job {
		t.Fatalf("Jobs = %+v", jobs)
	}
}

func TestDisabledQueueFailsLoudly(t *testing.T) {
	if err := NewQueue("disabled").EnqueueTranscode(context.Background(), TranscodeJob{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v", err)
	}
}

func TestNullPlaybackReturnsNoURLs(t *testing.T) {
	key := "videos/x/master.m3u8"
	p := NewPlaybackProvider()
	if p.PlaybackURL(&key) != nil || p.ThumbnailURL(&key) != nil {
		t.Fatal("expected nil URLs")
	}
}
