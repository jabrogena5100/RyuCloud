package integrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

// These tests talk to a real S3-compatible service (the MinIO from
// docker-compose.yml). They are opt-in because they need that service running:
//
//	docker compose up -d minio minio-init
//	docker compose run --rm -e S3_INTEGRATION_TEST=1 backend go test ./internal/integrations -run S3 -v
//
// Inside the backend container S3_ENDPOINT is http://minio:9000. The test signs
// URLs against that same endpoint (instead of the browser-facing
// S3_PUBLIC_ENDPOINT) so the Go test client can reach them.
func newTestS3(t *testing.T) *S3Storage {
	t.Helper()
	if os.Getenv("S3_INTEGRATION_TEST") != "1" {
		t.Skip("set S3_INTEGRATION_TEST=1 (with MinIO running) to run S3 integration tests")
	}
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		t.Fatal("S3_ENDPOINT is not set")
	}
	store, err := NewS3Storage(context.Background(), config.S3Config{
		Endpoint:        endpoint,
		PublicEndpoint:  endpoint,
		Region:          "us-east-1",
		Bucket:          os.Getenv("S3_BUCKET"),
		AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func randomTestKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "integration-tests/" + hex.EncodeToString(b)
}

func put(t *testing.T, up PresignedUpload, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(up.Method, up.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestS3PresignedUploadRoundTrip(t *testing.T) {
	store := newTestS3(t)
	ctx := context.Background()
	key := randomTestKey(t)
	t.Cleanup(func() { _ = store.DeleteObject(context.Background(), key) })
	body := []byte(strings.Repeat("ryucloud", 128))

	if _, err := store.StatObject(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("StatObject before upload = %v, want ErrObjectNotFound", err)
	}

	up, err := store.PresignUpload(ctx, PresignRequest{ObjectKey: key, ContentType: "video/mp4", SizeBytes: int64(len(body)), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if up.Method != http.MethodPut || !up.ExpiresAt.After(time.Now()) {
		t.Fatalf("unexpected presigned upload: %+v", up)
	}

	resp := put(t, up, body, up.Headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	info, err := store.StatObject(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(body)) || info.ContentType != "video/mp4" || info.ETag == "" {
		t.Fatalf("StatObject = %+v", info)
	}

	if err := store.DeleteObject(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StatObject(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("StatObject after delete = %v", err)
	}
	if err := store.DeleteObject(ctx, key); err != nil {
		t.Fatalf("deleting a missing object should succeed: %v", err)
	}
}

// The store itself must refuse requests that differ from what was presigned.
func TestS3PresignedUploadRejectsTampering(t *testing.T) {
	store := newTestS3(t)
	key := randomTestKey(t)
	t.Cleanup(func() { _ = store.DeleteObject(context.Background(), key) })
	body := []byte("0123456789")

	up, err := store.PresignUpload(context.Background(), PresignRequest{ObjectKey: key, ContentType: "video/mp4", SizeBytes: int64(len(body)), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	if resp := put(t, up, body, map[string]string{"Content-Type": "text/html"}); resp.StatusCode < 400 {
		t.Errorf("wrong content type was accepted: %d", resp.StatusCode)
	}
	if resp := put(t, up, append(body, 'x'), up.Headers); resp.StatusCode < 400 {
		t.Errorf("wrong body size was accepted: %d", resp.StatusCode)
	}
	if _, err := store.StatObject(context.Background(), key); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("a rejected upload left an object behind: %v", err)
	}
}

func TestS3PresignedUploadExpires(t *testing.T) {
	store := newTestS3(t)
	key := randomTestKey(t)
	t.Cleanup(func() { _ = store.DeleteObject(context.Background(), key) })

	up, err := store.PresignUpload(context.Background(), PresignRequest{ObjectKey: key, ContentType: "video/mp4", SizeBytes: 1, TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if resp := put(t, up, []byte("x"), up.Headers); resp.StatusCode < 400 {
		t.Errorf("an expired URL was accepted: %d", resp.StatusCode)
	}
}
