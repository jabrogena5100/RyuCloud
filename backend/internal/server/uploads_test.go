package server_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
)

// These tests drive the real HTTP API against real PostgreSQL. The object store
// is the in-memory fake; app.storage.Put plays the part of the browser's direct
// upload to the presigned URL.

const (
	mp4         = "video/mp4"
	declaredLen = 5_000_000
)

func uploadRequest(channelID string, extra map[string]any) map[string]any {
	body := map[string]any{
		"channel_id":   channelID,
		"title":        "My first upload",
		"description":  "A test video",
		"visibility":   "private",
		"filename":     "clip.mp4",
		"content_type": mp4,
		"size_bytes":   declaredLen,
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

type startedUpload struct {
	id, videoID, channelID string
	response               map[string]any
}

// startUpload initiates an upload for the browser's user and expects success.
func startUpload(t *testing.T, b *browser, channelID string, extra map[string]any) startedUpload {
	t.Helper()
	resp := b.post("/api/v1/uploads", uploadRequest(channelID, extra))
	expectStatus(t, resp, http.StatusCreated)
	out := resp.object(t)
	upload, _ := out["upload"].(map[string]any)
	video, _ := out["video"].(map[string]any)
	return startedUpload{id: str(upload, "id"), videoID: str(video, "id"), channelID: channelID, response: out}
}

func objectKeyOf(t *testing.T, uploadID string) string {
	t.Helper()
	var key string
	if err := testPool.QueryRow(context.Background(), `SELECT object_key FROM video_uploads WHERE id = $1`, uploadID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// browserUploads simulates the browser PUTting the file to the presigned URL.
func browserUploads(t *testing.T, app *testApp, uploadID, contentType string, size int64) string {
	t.Helper()
	key := objectKeyOf(t, uploadID)
	app.storage.Put(key, contentType, size)
	return key
}

type dbState struct {
	uploadStatus string
	videoStatus  string
	sourceKey    *string
	verifiedSize *int64
	completedAt  *time.Time
	reason       *string
}

func stateOf(t *testing.T, uploadID string) dbState {
	t.Helper()
	var s dbState
	err := testPool.QueryRow(context.Background(),
		`SELECT u.status, v.status, v.source_object_key, u.verified_size_bytes, u.completed_at, u.failure_reason
		 FROM video_uploads u JOIN videos v ON v.id = u.video_id WHERE u.id = $1`, uploadID,
	).Scan(&s.uploadStatus, &s.videoStatus, &s.sourceKey, &s.verifiedSize, &s.completedAt, &s.reason)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func countOf(t *testing.T, table string) int {
	t.Helper()
	var n int
	// table is always a constant from this file, never user input.
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func expectNoRows(t *testing.T) {
	t.Helper()
	if v, u := countOf(t, "videos"), countOf(t, "video_uploads"); v != 0 || u != 0 {
		t.Fatalf("expected no rows to be created, found %d videos and %d uploads", v, u)
	}
}

func uploadPath(id string) string { return "/api/v1/uploads/" + id }

// ---- happy path -------------------------------------------------------------

func TestUploadHappyPath(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)

	// 1. Initiate: a video and a pending upload are created, and a presigned target is returned.
	resp := alice.post("/api/v1/uploads", uploadRequest(channelID, map[string]any{"visibility": "public"}))
	expectStatus(t, resp, http.StatusCreated)
	if strings.Contains(string(resp.body), "object_key") {
		t.Error("the storage key must not be part of the API response")
	}
	out := resp.object(t)
	upload, _ := out["upload"].(map[string]any)
	video, _ := out["video"].(map[string]any)
	target, _ := out["target"].(map[string]any)
	if str(upload, "status") != "pending" || str(upload, "content_type") != mp4 || upload["size_bytes"] != float64(declaredLen) {
		t.Fatalf("unexpected upload: %v", upload)
	}
	if str(video, "status") != "uploading" || str(video, "visibility") != "public" || str(video, "title") != "My first upload" {
		t.Fatalf("unexpected video: %v", video)
	}
	headers, _ := target["headers"].(map[string]any)
	if str(target, "method") != "PUT" || str(target, "url") == "" || str(headers, "Content-Type") != mp4 || str(target, "expires_at") == "" {
		t.Fatalf("unexpected target: %v", target)
	}
	uploadID, videoID := str(upload, "id"), str(video, "id")

	// The API asked the store to sign exactly the validated key, type, size and TTL.
	presigned := app.storage.Presigned()
	if len(presigned) != 1 {
		t.Fatalf("presign requests = %d, want 1", len(presigned))
	}
	p := presigned[0]
	if !strings.HasPrefix(p.ObjectKey, "uploads/"+videoID+"/"+uploadID+"/") || p.ContentType != mp4 || p.SizeBytes != declaredLen || p.TTL != 15*time.Minute {
		t.Fatalf("unexpected presign request: %+v", p)
	}
	if got := stateOf(t, uploadID); got.uploadStatus != "pending" || got.videoStatus != "uploading" || got.sourceKey != nil {
		t.Fatalf("state after initiate: %+v", got)
	}

	// 2. The browser uploads straight to storage; 3. the frontend reports completion.
	key := browserUploads(t, app, uploadID, mp4, declaredLen)
	done := alice.post(uploadPath(uploadID)+"/complete", nil)
	expectStatus(t, done, http.StatusOK)
	if str(done.object(t), "status") != "uploaded" || str(done.object(t), "completed_at") == "" {
		t.Fatalf("unexpected completion response: %s", done.body)
	}

	// 4. PostgreSQL records a verified upload ready for processing.
	got := stateOf(t, uploadID)
	if got.uploadStatus != "uploaded" || got.videoStatus != "processing" {
		t.Fatalf("state after complete: %+v", got)
	}
	if got.sourceKey == nil || *got.sourceKey != key || got.verifiedSize == nil || *got.verifiedSize != declaredLen || got.completedAt == nil {
		t.Fatalf("verification not recorded: %+v", got)
	}

	// 5. Status retrieval and the owner's view of the video.
	status := alice.get(uploadPath(uploadID))
	expectStatus(t, status, http.StatusOK)
	if str(status.object(t), "status") != "uploaded" {
		t.Errorf("GET status: %s", status.body)
	}
	owned := alice.get("/api/v1/videos/" + videoID)
	expectStatus(t, owned, http.StatusOK)
	vid := owned.object(t)
	if str(vid, "status") != "processing" || vid["playback_url"] != nil {
		t.Errorf("video after upload should be processing and not playable: %v", vid)
	}

	// 6. Not playable, not listed: only transcoding (Phase 3) can make a video public.
	anon := app.browser()
	expectStatus(t, anon.get("/api/v1/videos/"+videoID), http.StatusNotFound)
	if n := len(items(t, anon.get("/api/v1/videos"))); n != 0 {
		t.Errorf("a processing video appeared in the public feed (%d items)", n)
	}
	if n := len(items(t, anon.get("/api/v1/channels/alice-tv/videos"))); n != 0 {
		t.Errorf("a processing video appeared on the public channel page (%d items)", n)
	}
}

func TestUploadAcceptsContentTypeParametersAndCase(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	for _, ct := range []string{"VIDEO/MP4", "video/mp4; codecs=avc1.42E01E"} {
		resp := alice.post("/api/v1/uploads", uploadRequest(channelID, map[string]any{"content_type": ct}))
		expectStatus(t, resp, http.StatusCreated)
		upload, _ := resp.object(t)["upload"].(map[string]any)
		if str(upload, "content_type") != mp4 {
			t.Errorf("%q stored as %q, want %q", ct, str(upload, "content_type"), mp4)
		}
	}
}

func TestUploadAllowedTypes(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	cases := []struct{ filename, contentType string }{
		{"a.mp4", "video/mp4"}, {"a.m4v", "video/mp4"}, {"a.mov", "video/quicktime"},
		{"a.webm", "video/webm"}, {"a.mkv", "video/x-matroska"},
	}
	for _, tc := range cases {
		resp := alice.post("/api/v1/uploads", uploadRequest(channelID, map[string]any{"filename": tc.filename, "content_type": tc.contentType}))
		if resp.status != http.StatusCreated {
			t.Errorf("%s as %s: got %d %s", tc.filename, tc.contentType, resp.status, resp.body)
		}
	}
}

func TestUploadLimitsEndpoint(t *testing.T) {
	resp := newTestApp(t).browser().get("/api/v1/uploads/limits")
	expectStatus(t, resp, http.StatusOK)
	limits := resp.object(t)
	if limits["max_bytes"] != float64(100<<20) || limits["url_ttl_seconds"] != float64(900) {
		t.Errorf("unexpected limits: %v", limits)
	}
	types, _ := limits["allowed_content_types"].([]any)
	if len(types) != 4 {
		t.Errorf("allowed_content_types = %v", types)
	}
}

// ---- authentication and ownership ------------------------------------------

func TestUploadEndpointsRequireAuthentication(t *testing.T) {
	anon := newTestApp(t).browser()
	calls := map[string]response{
		"initiate": anon.post("/api/v1/uploads", uploadRequest(missingUUID, nil)),
		"list":     anon.get("/api/v1/uploads"),
		"get":      anon.get(uploadPath(missingUUID)),
		"complete": anon.post(uploadPath(missingUUID)+"/complete", nil),
		"cancel":   anon.del(uploadPath(missingUUID)),
	}
	for name, resp := range calls {
		if resp.status != http.StatusUnauthorized || resp.errorCode(t) != "not_authenticated" {
			t.Errorf("%s: got %d %s", name, resp.status, resp.body)
		}
	}
	expectNoRows(t)
}

func TestCannotUploadToSomeoneElsesChannel(t *testing.T) {
	app := newTestApp(t)
	_, channelID := aliceWithChannel(app)
	resp := app.signedIn("bob@example.com").post("/api/v1/uploads", uploadRequest(channelID, nil))
	expectStatus(t, resp, http.StatusForbidden)
	expectNoRows(t)
}

func TestUploadToMissingChannel(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	expectStatus(t, b.post("/api/v1/uploads", uploadRequest(missingUUID, nil)), http.StatusNotFound)
	expectNoRows(t)
}

func TestUploadOwnershipIsEnforced(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen)
	bob := app.signedIn("bob@example.com")

	// Another user's upload is indistinguishable from a missing one.
	for name, resp := range map[string]response{
		"get":      bob.get(uploadPath(up.id)),
		"complete": bob.post(uploadPath(up.id)+"/complete", nil),
		"cancel":   bob.del(uploadPath(up.id)),
	} {
		if resp.status != http.StatusNotFound || resp.errorCode(t) != "upload_not_found" {
			t.Errorf("%s by another user: got %d %s", name, resp.status, resp.body)
		}
	}
	if n := len(items(t, bob.get("/api/v1/uploads"))); n != 0 {
		t.Errorf("bob's list shows %d of alice's uploads", n)
	}

	// Nothing about alice's upload changed.
	if got := stateOf(t, up.id); got.uploadStatus != "pending" || got.videoStatus != "uploading" {
		t.Fatalf("state changed by another user: %+v", got)
	}
	if !app.storage.Has(key) {
		t.Error("another user's cancel deleted the stored object")
	}
	// ... and she can still finish it.
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)
}

func TestMalformedUploadIDIsAValidationError(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	expectStatus(t, b.get(uploadPath("not-a-uuid")), http.StatusUnprocessableEntity)
	expectStatus(t, b.get(uploadPath(missingUUID)), http.StatusNotFound)
}

// ---- request validation -----------------------------------------------------

func TestUploadRejectsUnsupportedTypes(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	cases := map[string]map[string]any{
		"html":                   {"content_type": "text/html", "filename": "page.html"},
		"pdf":                    {"content_type": "application/pdf", "filename": "doc.pdf"},
		"generic binary":         {"content_type": "application/octet-stream", "filename": "clip.mp4"},
		"image":                  {"content_type": "image/png", "filename": "pic.png"},
		"executable as video":    {"filename": "malware.exe"},
		"extension of another":   {"filename": "clip.mov"},
		"no extension":           {"filename": "clip"},
		"double extension trick": {"filename": "clip.mp4.exe"},
	}
	for name, extra := range cases {
		resp := alice.post("/api/v1/uploads", uploadRequest(channelID, extra))
		if resp.status != http.StatusUnsupportedMediaType || resp.errorCode(t) != "unsupported_media_type" {
			t.Errorf("%s: got %d %s", name, resp.status, resp.body)
		}
	}
	expectNoRows(t)
}

func TestUploadRejectsOversizedFiles(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	limit := int64(100 << 20)

	resp := alice.post("/api/v1/uploads", uploadRequest(channelID, map[string]any{"size_bytes": limit + 1}))
	if resp.status != http.StatusRequestEntityTooLarge || resp.errorCode(t) != "file_too_large" {
		t.Fatalf("one byte over the limit: got %d %s", resp.status, resp.body)
	}
	expectNoRows(t)

	expectStatus(t, alice.post("/api/v1/uploads", uploadRequest(channelID, map[string]any{"size_bytes": limit})), http.StatusCreated)
}

func TestUploadValidatesFields(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	cases := []struct {
		name  string
		extra map[string]any
		field string
	}{
		{"zero size", map[string]any{"size_bytes": 0}, "size_bytes"},
		{"negative size", map[string]any{"size_bytes": -5}, "size_bytes"},
		{"fractional size", map[string]any{"size_bytes": 1.5}, "size_bytes"},
		{"string size", map[string]any{"size_bytes": "5000000"}, "size_bytes"},
		{"null size", map[string]any{"size_bytes": nil}, "size_bytes"},
		{"empty title", map[string]any{"title": "  "}, "title"},
		{"long title", map[string]any{"title": strings.Repeat("x", 101)}, "title"},
		{"empty filename", map[string]any{"filename": ""}, "filename"},
		{"path in filename", map[string]any{"filename": "../../etc/passwd.mp4"}, "filename"},
		{"control character in filename", map[string]any{"filename": "bad\x00.mp4"}, "filename"},
		{"empty content type", map[string]any{"content_type": ""}, "content_type"},
		{"bad visibility", map[string]any{"visibility": "secret"}, "visibility"},
		{"bad channel id", map[string]any{"channel_id": "nope"}, "channel_id"},
	}
	for _, tc := range cases {
		resp := alice.post("/api/v1/uploads", uploadRequest(channelID, tc.extra))
		if resp.status != http.StatusUnprocessableEntity || !resp.hasDetail(t, tc.field) {
			t.Errorf("%s: got %d %s", tc.name, resp.status, resp.body)
		}
	}
	expectNoRows(t)

	// A missing field is reported too.
	body := uploadRequest(channelID, nil)
	delete(body, "size_bytes")
	resp := alice.post("/api/v1/uploads", body)
	if resp.status != http.StatusUnprocessableEntity || !resp.hasDetail(t, "size_bytes") {
		t.Errorf("missing size: got %d %s", resp.status, resp.body)
	}
}

func TestUploadDefaultsToPrivate(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	body := uploadRequest(channelID, nil)
	delete(body, "visibility")
	resp := alice.post("/api/v1/uploads", body)
	expectStatus(t, resp, http.StatusCreated)
	video, _ := resp.object(t)["video"].(map[string]any)
	if str(video, "visibility") != "private" {
		t.Errorf("visibility = %q, want private", str(video, "visibility"))
	}
}

func TestUploadRejectsCrossOriginRequests(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	resp := alice.do(http.MethodPost, "/api/v1/uploads", uploadRequest(channelID, nil), map[string]string{"Origin": "https://evil.example"})
	expectStatus(t, resp, http.StatusForbidden)
	expectNoRows(t)
}

func TestTooManyPendingUploads(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	var first startedUpload
	for i := 0; i < 10; i++ {
		up := startUpload(t, alice, channelID, nil)
		if i == 0 {
			first = up
		}
	}
	resp := alice.post("/api/v1/uploads", uploadRequest(channelID, nil))
	if resp.status != http.StatusTooManyRequests || resp.errorCode(t) != "too_many_uploads" {
		t.Fatalf("11th pending upload: got %d %s", resp.status, resp.body)
	}
	if n := countOf(t, "video_uploads"); n != 10 {
		t.Errorf("uploads = %d, want 10", n)
	}

	expectStatus(t, alice.del(uploadPath(first.id)), http.StatusNoContent)
	expectStatus(t, alice.post("/api/v1/uploads", uploadRequest(channelID, nil)), http.StatusCreated)
}

// ---- completion -------------------------------------------------------------

func TestCompleteBeforeUploadFinishedCanBeRetried(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)

	resp := alice.post(uploadPath(up.id)+"/complete", nil)
	if resp.status != http.StatusConflict || resp.errorCode(t) != "upload_incomplete" {
		t.Fatalf("complete before upload: got %d %s", resp.status, resp.body)
	}
	if got := stateOf(t, up.id); got.uploadStatus != "pending" || got.videoStatus != "uploading" {
		t.Fatalf("an incomplete upload must stay pending: %+v", got)
	}

	browserUploads(t, app, up.id, mp4, declaredLen)
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)
}

func TestDuplicateCompletionIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, mp4, declaredLen)

	first := alice.post(uploadPath(up.id)+"/complete", nil)
	expectStatus(t, first, http.StatusOK)
	before := stateOf(t, up.id)

	for i := 0; i < 3; i++ {
		again := alice.post(uploadPath(up.id)+"/complete", nil)
		expectStatus(t, again, http.StatusOK)
		if str(again.object(t), "completed_at") != str(first.object(t), "completed_at") {
			t.Errorf("repeat %d changed completed_at", i)
		}
	}
	after := stateOf(t, up.id)
	if after.uploadStatus != "uploaded" || after.videoStatus != "processing" || !after.completedAt.Equal(*before.completedAt) {
		t.Fatalf("repeated completion changed state: before %+v after %+v", before, after)
	}
	if n := countOf(t, "video_uploads"); n != 1 {
		t.Errorf("uploads = %d, want 1", n)
	}
}

func TestConcurrentCompletionsAllSucceedOnce(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, mp4, declaredLen)

	const callers = 8
	statuses := make([]int, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, alice.base+uploadPath(up.id)+"/complete", nil)
			if err != nil {
				return
			}
			resp, err := alice.client.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			statuses[i] = resp.StatusCode
		}(i)
	}
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("concurrent complete #%d returned %d", i, status)
		}
	}
	if got := stateOf(t, up.id); got.uploadStatus != "uploaded" || got.videoStatus != "processing" {
		t.Fatalf("state after concurrent completes: %+v", got)
	}
}

func TestCompletionRejectsSizeMismatch(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen+1)

	resp := alice.post(uploadPath(up.id)+"/complete", nil)
	if resp.status != http.StatusUnprocessableEntity || resp.errorCode(t) != "upload_rejected" {
		t.Fatalf("size mismatch: got %d %s", resp.status, resp.body)
	}
	got := stateOf(t, up.id)
	if got.uploadStatus != "failed" || got.videoStatus != "failed" || got.reason == nil || got.sourceKey != nil {
		t.Fatalf("state after mismatch: %+v", got)
	}
	if app.storage.Has(key) {
		t.Error("the mismatched object should have been deleted")
	}

	// A failed upload stays failed.
	again := alice.post(uploadPath(up.id)+"/complete", nil)
	if again.status != http.StatusConflict || again.errorCode(t) != "upload_failed" {
		t.Errorf("complete after failure: got %d %s", again.status, again.body)
	}
}

func TestCompletionRejectsContentTypeMismatch(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, "text/html", declaredLen)

	resp := alice.post(uploadPath(up.id)+"/complete", nil)
	if resp.status != http.StatusUnprocessableEntity || resp.errorCode(t) != "upload_rejected" {
		t.Fatalf("type mismatch: got %d %s", resp.status, resp.body)
	}
	if got := stateOf(t, up.id); got.uploadStatus != "failed" || got.videoStatus != "failed" {
		t.Fatalf("state after mismatch: %+v", got)
	}
}

func TestCompletionAcceptsStoredContentTypeWithParameters(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, "video/mp4; charset=binary", declaredLen)
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)
}

// ---- expiry -----------------------------------------------------------------

func TestExpiredUploadsAreMarkedAndCannotComplete(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen) // uploaded but never reported
	execSQL(t, `UPDATE video_uploads SET expires_at = now() - interval '3 hours' WHERE id = $1`, up.id)

	status := alice.get(uploadPath(up.id))
	expectStatus(t, status, http.StatusOK)
	if str(status.object(t), "status") != "expired" || status.object(t)["failure_reason"] == nil {
		t.Fatalf("expected an expired upload with a reason: %s", status.body)
	}
	if got := stateOf(t, up.id); got.uploadStatus != "expired" || got.videoStatus != "failed" {
		t.Fatalf("state after expiry: %+v", got)
	}
	if app.storage.Has(key) {
		t.Error("the abandoned object should have been deleted")
	}

	resp := alice.post(uploadPath(up.id)+"/complete", nil)
	if resp.status != http.StatusGone || resp.errorCode(t) != "upload_expired" {
		t.Fatalf("complete after expiry: got %d %s", resp.status, resp.body)
	}
}

func TestCompleteStillWorksInsideTheGracePeriod(t *testing.T) {
	// The presigned URL has expired, but a slow upload that started in time may still finish.
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, mp4, declaredLen)
	execSQL(t, `UPDATE video_uploads SET expires_at = now() - interval '30 minutes' WHERE id = $1`, up.id)
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)
}

func TestExpiredUploadsFreeUpTheVideoForCleanup(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	execSQL(t, `UPDATE video_uploads SET expires_at = now() - interval '3 hours' WHERE id = $1`, up.id)

	list := alice.get("/api/v1/uploads?status=expired")
	expectStatus(t, list, http.StatusOK)
	if n := len(items(t, list)); n != 1 {
		t.Fatalf("expired uploads listed = %d, want 1", n)
	}
	// The failed video can be deleted like any other.
	expectStatus(t, alice.del("/api/v1/videos/"+up.videoID), http.StatusNoContent)
}

// ---- cancellation -----------------------------------------------------------

func TestCancelPendingUpload(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen)

	expectStatus(t, alice.del(uploadPath(up.id)), http.StatusNoContent)
	if got := stateOf(t, up.id); got.uploadStatus != "cancelled" || got.videoStatus != "draft" {
		t.Fatalf("state after cancel: %+v", got)
	}
	if app.storage.Has(key) {
		t.Error("cancelling should delete the stored object")
	}

	// Cancelling again is a no-op; completing a cancelled upload is a conflict.
	expectStatus(t, alice.del(uploadPath(up.id)), http.StatusNoContent)
	resp := alice.post(uploadPath(up.id)+"/complete", nil)
	if resp.status != http.StatusConflict || resp.errorCode(t) != "upload_cancelled" {
		t.Errorf("complete after cancel: got %d %s", resp.status, resp.body)
	}
}

func TestCannotCancelACompletedUpload(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen)
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)

	resp := alice.del(uploadPath(up.id))
	if resp.status != http.StatusConflict || resp.errorCode(t) != "upload_already_completed" {
		t.Fatalf("cancel after complete: got %d %s", resp.status, resp.body)
	}
	if got := stateOf(t, up.id); got.uploadStatus != "uploaded" || got.videoStatus != "processing" || !app.storage.Has(key) {
		t.Fatalf("a rejected cancel changed state: %+v", got)
	}
}

// ---- storage failures -------------------------------------------------------

func TestStorageFailureWhilePresigningCreatesNothing(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	app.storage.FailWith(integrations.OpPresign, errors.New("secret internal storage detail"))

	resp := alice.post("/api/v1/uploads", uploadRequest(channelID, nil))
	if resp.status != http.StatusBadGateway || resp.errorCode(t) != "storage_unavailable" {
		t.Fatalf("got %d %s", resp.status, resp.body)
	}
	if strings.Contains(string(resp.body), "secret internal") {
		t.Error("storage error details leaked to the client")
	}
	expectNoRows(t) // the transaction rolled back

	app.storage.FailWith(integrations.OpPresign, nil)
	expectStatus(t, alice.post("/api/v1/uploads", uploadRequest(channelID, nil)), http.StatusCreated)
}

func TestStorageFailureWhileVerifyingKeepsUploadPending(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, mp4, declaredLen)
	app.storage.FailWith(integrations.OpStat, errors.New("connection refused"))

	resp := alice.post(uploadPath(up.id)+"/complete", nil)
	if resp.status != http.StatusBadGateway || resp.errorCode(t) != "storage_unavailable" {
		t.Fatalf("got %d %s", resp.status, resp.body)
	}
	if got := stateOf(t, up.id); got.uploadStatus != "pending" || got.videoStatus != "uploading" {
		t.Fatalf("a storage outage must not change the upload: %+v", got)
	}

	app.storage.FailWith(integrations.OpStat, nil)
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)
}

func TestStorageDeleteFailureDoesNotBlockCancel(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen)
	app.storage.FailWith(integrations.OpDelete, errors.New("timeout"))

	expectStatus(t, alice.del(uploadPath(up.id)), http.StatusNoContent)
	if got := stateOf(t, up.id); got.uploadStatus != "cancelled" {
		t.Fatalf("state = %+v", got)
	}
	if !app.storage.Has(key) {
		t.Error("expected the object to remain because deletion failed")
	}
}

func TestUploadsReportStorageNotConfigured(t *testing.T) {
	app := newTestAppWithStorage(t, integrations.DisabledStorage{})
	alice, channelID := aliceWithChannel(app)
	resp := alice.post("/api/v1/uploads", uploadRequest(channelID, nil))
	if resp.status != http.StatusServiceUnavailable || resp.errorCode(t) != "storage_not_configured" {
		t.Fatalf("got %d %s", resp.status, resp.body)
	}
	expectNoRows(t)
}

// ---- listing and video deletion ----------------------------------------------

func TestListUploads(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	first := startUpload(t, alice, channelID, map[string]any{"title": "one"})
	second := startUpload(t, alice, channelID, map[string]any{"title": "two"})
	browserUploads(t, app, second.id, mp4, declaredLen)
	expectStatus(t, alice.post(uploadPath(second.id)+"/complete", nil), http.StatusOK)

	all := items(t, alice.get("/api/v1/uploads"))
	if len(all) != 2 {
		t.Fatalf("uploads = %d, want 2", len(all))
	}
	pending := items(t, alice.get("/api/v1/uploads?status=pending"))
	if len(pending) != 1 || str(pending[0].(map[string]any), "id") != first.id || str(pending[0].(map[string]any), "video_id") != first.videoID {
		t.Fatalf("unexpected pending list: %v", pending)
	}
	expectStatus(t, alice.get("/api/v1/uploads?status=bogus"), http.StatusUnprocessableEntity)
	expectStatus(t, alice.get("/api/v1/uploads?limit=0"), http.StatusUnprocessableEntity)
}

func TestDeletingAVideoDeletesItsStoredObjects(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	key := browserUploads(t, app, up.id, mp4, declaredLen)
	expectStatus(t, alice.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)

	expectStatus(t, alice.del("/api/v1/videos/"+up.videoID), http.StatusNoContent)
	if app.storage.Has(key) {
		t.Error("the source object should be deleted with its video")
	}
	if countOf(t, "video_uploads") != 0 {
		t.Error("upload records should cascade with the video")
	}
	expectStatus(t, alice.get(uploadPath(up.id)), http.StatusNotFound)
}

func TestDeletingAVideoSucceedsEvenIfStorageIsDown(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	up := startUpload(t, alice, channelID, nil)
	browserUploads(t, app, up.id, mp4, declaredLen)
	app.storage.FailWith(integrations.OpDelete, errors.New("down"))
	expectStatus(t, alice.del("/api/v1/videos/"+up.videoID), http.StatusNoContent)
}

// ---- database consistency ---------------------------------------------------

func insertVideoRow(t *testing.T, channelID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO videos (channel_id, title, status) VALUES ($1, 'DB video', 'uploading') RETURNING id`, channelID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertUploadRow(userID, videoID, key, status string, verified any, completed string) error {
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO video_uploads
		   (video_id, user_id, object_key, original_filename, content_type, declared_size_bytes, status, verified_size_bytes, completed_at, expires_at)
		 VALUES ($1, $2, $3, 'a.mp4', 'video/mp4', 100, $4, $5, `+completed+`, now() + interval '1 hour')`,
		videoID, userID, key, status, verified)
	return err
}

func TestUploadTableConstraints(t *testing.T) {
	truncateTables(t)
	userID := insertUser(t, "db@example.com")
	videoID := insertVideoRow(t, insertChannel(t, userID))

	cases := []struct {
		name string
		err  error
		code string
	}{
		{"unknown status", insertUploadRow(userID, videoID, "k/1", "bogus", nil, "NULL"), "23514"},
		{"uploaded without verification", insertUploadRow(userID, videoID, "k/2", "uploaded", nil, "NULL"), "23514"},
		{"uploaded with a different verified size", insertUploadRow(userID, videoID, "k/3", "uploaded", 99, "now()"), "23514"},
		{"valid pending upload", insertUploadRow(userID, videoID, "k/4", "pending", nil, "NULL"), ""},
		{"second pending upload for one video", insertUploadRow(userID, videoID, "k/5", "pending", nil, "NULL"), "23505"},
		{"duplicate object key", insertUploadRow(userID, insertVideoRow(t, mustChannelOf(t, videoID)), "k/4", "pending", nil, "NULL"), "23505"},
		{"valid verified upload", insertUploadRow(userID, insertVideoRow(t, mustChannelOf(t, videoID)), "k/6", "uploaded", 100, "now()"), ""},
	}
	for _, tc := range cases {
		if got := pgCode(tc.err); got != tc.code {
			t.Errorf("%s: SQLSTATE %q, want %q (err: %v)", tc.name, got, tc.code, tc.err)
		}
	}
}

func mustChannelOf(t *testing.T, videoID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT channel_id FROM videos WHERE id = $1`, videoID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDeletingUserOrVideoCascadesToUploads(t *testing.T) {
	truncateTables(t)
	userID := insertUser(t, "db@example.com")
	channelID := insertChannel(t, userID)
	v1, v2 := insertVideoRow(t, channelID), insertVideoRow(t, channelID)
	if err := insertUploadRow(userID, v1, "k/a", "pending", nil, "NULL"); err != nil {
		t.Fatal(err)
	}
	if err := insertUploadRow(userID, v2, "k/b", "pending", nil, "NULL"); err != nil {
		t.Fatal(err)
	}

	execSQL(t, `DELETE FROM videos WHERE id = $1`, v1)
	if n := countOf(t, "video_uploads"); n != 1 {
		t.Fatalf("uploads after deleting a video = %d, want 1", n)
	}
	execSQL(t, `DELETE FROM users WHERE id = $1`, userID)
	if n := countOf(t, "video_uploads"); n != 0 {
		t.Fatalf("uploads after deleting the user = %d, want 0", n)
	}
}
