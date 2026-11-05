package server_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/jobs"
	"github.com/jabrogena5100/RyuCloud/backend/internal/transcode"
	"github.com/jabrogena5100/RyuCloud/backend/internal/worker"
)

// These tests run the real worker, job store, PostgreSQL schema, queue semantics and
// API handlers together. Only two things are replaced: object storage is in memory,
// and FFmpeg is a fake that writes small placeholder HLS files (the real binaries are
// covered by internal/transcode's tests and scripts/e2e-processing-test.sh). That lets
// every failure path (crashes, storage errors, lease loss) be triggered on demand.

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// ---- fake media tool ---------------------------------------------------------

type fakeMedia struct {
	mu           sync.Mutex
	probe        transcode.Probe
	probeErr     error
	transcodeErr error
	thumbErr     error
	// transcodeHook runs at the start of every Transcode; returning an error fails it.
	transcodeHook func(ctx context.Context, req transcode.Request) error
	probes        int
	transcodes    int
	thumbs        int
}

func newFakeMedia() *fakeMedia {
	return &fakeMedia{probe: transcode.Probe{Width: 1280, Height: 720, DurationSeconds: 10, HasAudio: true, VideoCodec: "h264"}}
}

func (f *fakeMedia) Probe(_ context.Context, _ string) (transcode.Probe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return f.probe, f.probeErr
}

func (f *fakeMedia) Transcode(ctx context.Context, req transcode.Request) error {
	f.mu.Lock()
	f.transcodes++
	hook, fail := f.transcodeHook, f.transcodeErr
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, req); err != nil {
			return err
		}
	}
	if fail != nil {
		return fail
	}
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return err
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000000,\nseg_00000.ts\n#EXTINF:4.000000,\nseg_00001.ts\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(req.OutDir+"/index.m3u8", []byte(playlist), 0o644); err != nil {
		return err
	}
	for _, seg := range []string{"seg_00000.ts", "seg_00001.ts"} {
		data := append([]byte{0x47}, []byte(strings.Repeat("x", 999))...) // starts with the MPEG-TS sync byte
		if err := os.WriteFile(req.OutDir+"/"+seg, data, 0o644); err != nil {
			return err
		}
	}
	if req.OnProgress != nil {
		req.OnProgress(0.5)
		req.OnProgress(1)
	}
	return nil
}

func (f *fakeMedia) Thumbnail(_ context.Context, _, dst string, _ float64) error {
	f.mu.Lock()
	f.thumbs++
	err := f.thumbErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(dst, []byte{0xff, 0xd8, 0xff, 0xe0, 'J', 'F', 'I', 'F'}, 0o644)
}

func (f *fakeMedia) set(fn func(*fakeMedia)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeMedia) transcodeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transcodes
}

// ---- harness -----------------------------------------------------------------

type harness struct {
	t       *testing.T
	app     *testApp
	owner   *browser
	channel string
	media   *fakeMedia
	store   *jobs.Store
	tmp     string
	n       int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	app := newTestApp(t)
	owner, channelID := aliceWithChannel(app)
	return &harness{t: t, app: app, owner: owner, channel: channelID, media: newFakeMedia(), store: jobs.NewStore(testPool), tmp: t.TempDir()}
}

func (h *harness) config() config.WorkerConfig {
	return config.WorkerConfig{
		Concurrency:   1,
		MaxAttempts:   4,
		RetryBase:     time.Hour, // tests move next_run_at explicitly instead of waiting
		RetryMax:      time.Hour,
		Lease:         10 * time.Second,
		SweepInterval: 20 * time.Millisecond,
		RepublishAge:  time.Millisecond,
		ShutdownGrace: 2 * time.Second,
		Timeout:       30 * time.Second,
		MaxDuration:   2 * time.Hour,
		FFmpegThreads: 1,
		TempDir:       h.tmp,
	}
}

func (h *harness) newWorker(id string, mutate ...func(*config.WorkerConfig)) *worker.Worker {
	cfg := h.config()
	for _, m := range mutate {
		m(&cfg)
	}
	return worker.New(worker.Deps{
		Store: jobs.NewStore(testPool), Storage: h.app.storage, Queue: h.app.queue, Media: h.media,
		Config: cfg, Logger: discardLogger(), WorkerID: id, Jitter: func() float64 { return 0.5 },
	})
}

type testVideo struct {
	id, uploadID, jobID, sourceKey string
}

// newVideo uploads a file (2 KiB of bytes in memory storage) and completes the upload
// through the real API, which creates the processing job and publishes it.
func (h *harness) newVideo(visibility string) testVideo {
	h.t.Helper()
	h.n++
	up := startUpload(h.t, h.owner, h.channel, map[string]any{"visibility": visibility, "size_bytes": 2048, "title": "Video " + string(rune('A'+h.n))})
	key := objectKeyOf(h.t, up.id)
	h.app.storage.PutData(key, mp4, make([]byte, 2048))
	expectStatus(h.t, h.owner.post(uploadPath(up.id)+"/complete", nil), http.StatusOK)

	v := testVideo{id: up.videoID, uploadID: up.id, sourceKey: key}
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM processing_jobs WHERE video_id = $1`, v.id).Scan(&v.jobID); err != nil {
		h.t.Fatalf("no job was created for the video: %v", err)
	}
	return v
}

type videoRow struct {
	status   string
	errMsg   *string
	manifest *string
	thumb    *string
	duration *int
	pubAt    *time.Time
}

func videoStateOf(t *testing.T, videoID string) videoRow {
	t.Helper()
	var r videoRow
	err := testPool.QueryRow(context.Background(),
		`SELECT status, processing_error, hls_manifest_key, thumbnail_object_key, duration_seconds, published_at FROM videos WHERE id = $1`, videoID,
	).Scan(&r.status, &r.errMsg, &r.manifest, &r.thumb, &r.duration, &r.pubAt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (h *harness) job(v testVideo) jobs.Snapshot {
	h.t.Helper()
	sn, err := h.store.Get(context.Background(), v.id)
	if err != nil {
		h.t.Fatal(err)
	}
	return sn
}

func (h *harness) makeDue(v testVideo) {
	h.t.Helper()
	execSQL(h.t, `UPDATE processing_jobs SET next_run_at = now() - interval '1 second' WHERE id = $1`, v.jobID)
}

func (h *harness) expireLease(v testVideo) {
	h.t.Helper()
	execSQL(h.t, `UPDATE processing_jobs SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, v.jobID)
}

func (h *harness) process(w *worker.Worker, v testVideo) {
	h.t.Helper()
	if err := w.ProcessJob(context.Background(), v.jobID); err != nil {
		h.t.Fatalf("ProcessJob returned an error (the outcome was not persisted): %v", err)
	}
}

func (h *harness) leftoverTempDirs() []string {
	entries, _ := os.ReadDir(h.tmp)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func outputKeys(h *harness, v testVideo) []string {
	return h.app.storage.KeysWithPrefix(integrations.VideoPrefix(v.id))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- the API side: queuing --------------------------------------------------

func TestCompletingAnUploadCreatesAndPublishesAJob(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")

	if got := videoStateOf(t, v.id); got.status != "queued" {
		t.Fatalf("video status = %q, want queued", got.status)
	}
	sn := h.job(v)
	if sn.State != "queued" || sn.Attempts != 0 || sn.MaxAttempts != 4 || sn.Progress != 0 {
		t.Fatalf("unexpected job: %+v", sn)
	}
	if published := h.app.queue.Published(); len(published) != 1 || published[0] != v.jobID {
		t.Fatalf("published = %v, want [%s]", published, v.jobID)
	}
	var markedPublished bool
	if err := testPool.QueryRow(context.Background(), `SELECT last_published_at IS NOT NULL FROM processing_jobs WHERE id = $1`, v.jobID).Scan(&markedPublished); err != nil || !markedPublished {
		t.Errorf("the publication should be recorded (err %v)", err)
	}

	// Completing again is idempotent: no second job, no second message.
	expectStatus(t, h.owner.post(uploadPath(v.uploadID)+"/complete", nil), http.StatusOK)
	if n := countOf(t, "processing_jobs"); n != 1 {
		t.Errorf("jobs = %d, want 1", n)
	}
	if n := len(h.app.queue.Published()); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
}

func TestVideoAPIShowsQueuedStatusAndProgress(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")

	resp := h.owner.get("/api/v1/videos/" + v.id)
	expectStatus(t, resp, http.StatusOK)
	body := resp.object(t)
	if str(body, "status") != "queued" || body["processing_progress"] != float64(0) || body["processing_error"] != nil || body["playback_url"] != nil {
		t.Fatalf("unexpected video: %v", body)
	}

	execSQL(t, `UPDATE processing_jobs SET state = 'processing', locked_by = 'w', lease_expires_at = now() + interval '1 minute', progress = 42 WHERE id = $1`, v.jobID)
	execSQL(t, `UPDATE videos SET status = 'processing' WHERE id = $1`, v.id)
	body = h.owner.get("/api/v1/videos/" + v.id).object(t)
	if str(body, "status") != "processing" || body["processing_progress"] != float64(42) {
		t.Fatalf("progress not exposed while processing: %v", body)
	}
}

func TestCompleteSucceedsEvenIfTheQueueIsDownAndASweepRecovers(t *testing.T) {
	h := newHarness(t)
	h.app.queue.FailWith(errors.New("redis is down"))

	v := h.newVideo("private") // completes with HTTP 200 despite the failed publish
	if got := videoStateOf(t, v.id); got.status != "queued" {
		t.Fatalf("status = %q", got.status)
	}
	var marked bool
	_ = testPool.QueryRow(context.Background(), `SELECT last_published_at IS NOT NULL FROM processing_jobs WHERE id = $1`, v.jobID).Scan(&marked)
	if marked {
		t.Error("an unpublished job must not be marked as published")
	}

	// Redis comes back; a worker's sweep finds the orphaned job and sends it.
	h.app.queue.FailWith(nil)
	w := h.newWorker("sweeper")
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if published := h.app.queue.Published(); len(published) != 1 || published[0] != v.jobID {
		t.Fatalf("after the sweep, published = %v", published)
	}
}

// ---- success path ------------------------------------------------------------

func TestWorkerProcessesAVideoToReady(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("public")
	w := h.newWorker("worker-1")

	h.process(w, v)

	got := videoStateOf(t, v.id)
	if got.status != "ready" || got.errMsg != nil || got.pubAt == nil || got.duration == nil || *got.duration != 10 {
		t.Fatalf("video after processing: %+v", got)
	}
	if got.manifest == nil || *got.manifest != integrations.HLSMasterKey(v.id) || got.thumb == nil || *got.thumb != integrations.ThumbnailKey(v.id) {
		t.Fatalf("asset keys not recorded: manifest=%v thumb=%v", got.manifest, got.thumb)
	}
	sn := h.job(v)
	if sn.State != "succeeded" || sn.Progress != 100 || sn.Attempts != 1 || sn.LockedBy != nil || sn.FinishedAt == nil {
		t.Fatalf("job after processing: %+v", sn)
	}

	// 720p source: both renditions, recorded with their real measurements.
	rows, err := testPool.Query(context.Background(), `SELECT name, width, height, segment_count, bandwidth_bps, size_bytes, playlist_key FROM video_renditions WHERE video_id = $1 ORDER BY height`, v.id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name, key string
		var width, height, segs int
		var bps, size int64
		if err := rows.Scan(&name, &width, &height, &segs, &bps, &size, &key); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		if segs != 2 || bps <= 0 || size < 2000 || key != integrations.HLSKey(v.id, name+"/index.m3u8") {
			t.Errorf("rendition %s: segments=%d bandwidth=%d size=%d key=%s", name, segs, bps, size, key)
		}
	}
	if strings.Join(names, ",") != "360p,720p" {
		t.Fatalf("renditions = %v", names)
	}

	// Storage layout: the original is kept, the generated files sit under videos/{id}/.
	want := []string{
		"videos/" + v.id + "/hls/360p/index.m3u8", "videos/" + v.id + "/hls/360p/seg_00000.ts", "videos/" + v.id + "/hls/360p/seg_00001.ts",
		"videos/" + v.id + "/hls/720p/index.m3u8", "videos/" + v.id + "/hls/720p/seg_00000.ts", "videos/" + v.id + "/hls/720p/seg_00001.ts",
		"videos/" + v.id + "/hls/master.m3u8", "videos/" + v.id + "/thumbnail.jpg",
	}
	if got := strings.Join(outputKeys(h, v), "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("stored objects:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if !h.app.storage.Has(v.sourceKey) {
		t.Error("the original upload must be kept")
	}
	master, _ := h.app.storage.Data(integrations.HLSMasterKey(v.id))
	for _, line := range []string{"#EXTM3U", "RESOLUTION=640x360", "360p/index.m3u8", "RESOLUTION=1280x720", "720p/index.m3u8"} {
		if !strings.Contains(string(master), line) {
			t.Errorf("master playlist missing %q:\n%s", line, master)
		}
	}
	if ct := h.app.storage.ContentType(integrations.HLSKey(v.id, "360p/seg_00000.ts")); ct != "video/mp2t" {
		t.Errorf("segment content type = %q", ct)
	}
	if ct := h.app.storage.ContentType(integrations.HLSMasterKey(v.id)); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("playlist content type = %q", ct)
	}
	if left := h.leftoverTempDirs(); len(left) != 0 {
		t.Errorf("temporary files were not cleaned up: %v", left)
	}

	// The API now reports a playable, listed video.
	api := h.owner.get("/api/v1/videos/" + v.id).object(t)
	if str(api, "status") != "ready" || str(api, "playback_url") != "/api/v1/videos/"+v.id+"/hls/master.m3u8" ||
		str(api, "thumbnail_url") != "/api/v1/videos/"+v.id+"/thumbnail" || api["processing_progress"] != nil || api["duration_seconds"] != float64(10) {
		t.Fatalf("unexpected API video: %v", api)
	}
	if n := len(items(t, h.app.browser().get("/api/v1/videos"))); n != 1 {
		t.Errorf("public feed lists %d videos, want 1", n)
	}
}

func TestProcessingReportsProgressAndOnlyAdvances(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	var seen []int
	h.media.set(func(f *fakeMedia) {
		f.transcodeHook = func(ctx context.Context, req transcode.Request) error {
			sn, err := h.store.Get(ctx, v.id)
			if err == nil {
				seen = append(seen, sn.Progress)
			}
			return nil
		}
	})
	h.process(h.newWorker("w"), v)
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Errorf("progress went backwards: %v", seen)
		}
	}
	if len(seen) == 0 || seen[0] == 0 {
		t.Errorf("progress should be reported before transcoding starts: %v", seen)
	}
}

func TestSmallSourceIsNotUpscaled(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.media.set(func(f *fakeMedia) { f.probe = transcode.Probe{Width: 320, Height: 240, DurationSeconds: 3} })
	h.process(h.newWorker("w"), v)

	var names string
	_ = testPool.QueryRow(context.Background(), `SELECT string_agg(name, ',' ORDER BY height) FROM video_renditions WHERE video_id = $1`, v.id).Scan(&names)
	if names != "240p" {
		t.Fatalf("renditions = %q, want only 240p", names)
	}
}

func TestMissingThumbnailDoesNotFailAPlayableVideo(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.media.set(func(f *fakeMedia) { f.thumbErr = errors.New("no frame") })
	h.process(h.newWorker("w"), v)

	got := videoStateOf(t, v.id)
	if got.status != "ready" || got.thumb != nil || got.manifest == nil {
		t.Fatalf("video = %+v", got)
	}
	if h.app.storage.Has(integrations.ThumbnailKey(v.id)) {
		t.Error("no thumbnail object should exist")
	}
	if h.media.thumbs != 2 {
		t.Errorf("thumbnail attempts = %d, want 2 (requested position, then the first frame)", h.media.thumbs)
	}
	expectStatus(t, h.owner.get("/api/v1/videos/"+v.id+"/thumbnail"), http.StatusNotFound)
}

// ---- permanent failures ------------------------------------------------------

func TestInvalidSourceMediaFailsWithoutRetrying(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.media.set(func(f *fakeMedia) {
		f.probeErr = &transcode.InvalidMediaError{Reason: "the file could not be read as a video"}
	})
	h.process(h.newWorker("w"), v)

	got := videoStateOf(t, v.id)
	if got.status != "failed" || got.errMsg == nil || !strings.Contains(*got.errMsg, "couldn't be processed") {
		t.Fatalf("video = %+v", got)
	}
	sn := h.job(v)
	if sn.State != "failed" || sn.Attempts != 1 || sn.LastError == nil {
		t.Fatalf("a corrupt file must fail on the first attempt: %+v", sn)
	}
	if h.media.transcodeCalls() != 0 {
		t.Error("nothing should be transcoded for an invalid source")
	}
	if keys := outputKeys(h, v); len(keys) != 0 {
		t.Errorf("failed processing left outputs: %v", keys)
	}
	api := h.owner.get("/api/v1/videos/" + v.id).object(t)
	if str(api, "status") != "failed" || str(api, "processing_error") == "" {
		t.Errorf("the owner should see why it failed: %v", api)
	}
	// And nothing will ever pick it up again.
	if err := h.newWorker("sweeper").Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(h.app.queue.Published()); n != 1 {
		t.Errorf("a failed job was re-sent to the queue (%d messages)", n)
	}
}

func TestUnsupportedFormatsAreRejectedBeforeAnyTranscoding(t *testing.T) {
	cases := map[string]transcode.Probe{
		"longer than the limit": {Width: 1280, Height: 720, DurationSeconds: 3 * 3600},
		"too large":             {Width: 20000, Height: 20000, DurationSeconds: 10},
		"no readable duration":  {Width: 1280, Height: 720},
		"degenerate size":       {Width: 2, Height: 2, DurationSeconds: 10},
	}
	for name, probe := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			v := h.newVideo("private")
			h.media.set(func(f *fakeMedia) { f.probe = probe })
			h.process(h.newWorker("w"), v)

			if got := videoStateOf(t, v.id); got.status != "failed" || got.errMsg == nil {
				t.Fatalf("video = %+v", got)
			}
			if sn := h.job(v); sn.State != "failed" || sn.Attempts != 1 {
				t.Fatalf("job = %+v", sn)
			}
			if h.media.transcodeCalls() != 0 {
				t.Error("an unsupported source must not reach FFmpeg")
			}
		})
	}
}

func TestMissingSourceObjectFailsPermanently(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	_ = h.app.storage.DeleteObject(context.Background(), v.sourceKey)
	h.process(h.newWorker("w"), v)

	got := videoStateOf(t, v.id)
	if got.status != "failed" || got.errMsg == nil || !strings.Contains(*got.errMsg, "could not be found") {
		t.Fatalf("video = %+v", got)
	}
	if sn := h.job(v); sn.State != "failed" || sn.Attempts != 1 {
		t.Fatalf("job = %+v", sn)
	}
}

// ---- retries -----------------------------------------------------------------

func TestTransientFailureIsRetriedWithBackoffThenSucceeds(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	w := h.newWorker("w")
	h.media.set(func(f *fakeMedia) { f.transcodeErr = errors.New("ffmpeg was killed (out of memory)") })

	h.process(w, v)
	sn := h.job(v)
	if sn.State != "retry" || sn.Attempts != 1 || sn.LastError == nil || !strings.Contains(*sn.LastError, "out of memory") {
		t.Fatalf("after the first failure: %+v", sn)
	}
	if !sn.NextRunAt.After(time.Now().Add(30 * time.Minute)) {
		t.Errorf("the retry should be scheduled with backoff, next_run_at = %v", sn.NextRunAt)
	}
	if got := videoStateOf(t, v.id); got.status != "queued" || got.errMsg != nil {
		t.Fatalf("a retrying video goes back to queued without an error: %+v", got)
	}

	// Not due yet: a duplicate message does nothing, and the sweeper does not resend it.
	before := h.media.transcodeCalls()
	h.process(w, v)
	if h.media.transcodeCalls() != before || h.job(v).Attempts != 1 {
		t.Fatal("a job must not run before its backoff has elapsed")
	}
	sent := len(h.app.queue.Published())
	_ = w.Sweep(context.Background())
	if len(h.app.queue.Published()) != sent {
		t.Error("the sweeper must not resend a job whose backoff has not elapsed")
	}

	// Backoff over: the sweeper resends it and the next attempt succeeds.
	h.makeDue(v)
	h.media.set(func(f *fakeMedia) { f.transcodeErr = nil })
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.app.queue.Published()) != sent+1 {
		t.Fatalf("expected the due job to be resent, published = %v", h.app.queue.Published())
	}
	h.process(w, v)
	if got := videoStateOf(t, v.id); got.status != "ready" {
		t.Fatalf("video = %+v", got)
	}
	if sn := h.job(v); sn.State != "succeeded" || sn.Attempts != 2 {
		t.Fatalf("job = %+v", sn)
	}
}

func TestRetryDelaysGrowExponentially(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.media.set(func(f *fakeMedia) { f.transcodeErr = errors.New("boom") })
	w := h.newWorker("w", func(c *config.WorkerConfig) {
		c.RetryBase, c.RetryMax = 100*time.Second, 10*time.Hour
	})

	var delays []time.Duration
	for attempt := 1; attempt <= 3; attempt++ {
		h.makeDue(v)
		h.process(w, v)
		var delay float64
		_ = testPool.QueryRow(context.Background(), `SELECT extract(epoch FROM next_run_at - now()) FROM processing_jobs WHERE id = $1`, v.jobID).Scan(&delay)
		delays = append(delays, time.Duration(delay*float64(time.Second)).Round(10*time.Second))
	}
	// 100s, 200s, 400s (jitter is pinned to the centre of its window in tests).
	want := []time.Duration{100 * time.Second, 200 * time.Second, 400 * time.Second}
	for i := range want {
		if delays[i] < want[i]-20*time.Second || delays[i] > want[i] {
			t.Errorf("retry %d delay = %v, want about %v", i+1, delays[i], want[i])
		}
	}
}

func TestRetryExhaustionFailsTheVideo(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.media.set(func(f *fakeMedia) { f.transcodeErr = errors.New("always broken") })
	execSQL(t, `UPDATE processing_jobs SET max_attempts = 3 WHERE id = $1`, v.jobID)
	w := h.newWorker("w")

	for attempt := 1; attempt <= 3; attempt++ {
		h.makeDue(v)
		h.process(w, v)
		sn := h.job(v)
		if attempt < 3 && (sn.State != "retry" || sn.Attempts != attempt) {
			t.Fatalf("attempt %d: %+v", attempt, sn)
		}
	}

	sn := h.job(v)
	if sn.State != "failed" || sn.Attempts != 3 || sn.LastError == nil || !strings.Contains(*sn.LastError, "attempts exhausted") {
		t.Fatalf("after exhausting retries: %+v", sn)
	}
	got := videoStateOf(t, v.id)
	if got.status != "failed" || got.errMsg == nil || !strings.Contains(*got.errMsg, "several attempts") {
		t.Fatalf("video = %+v", got)
	}

	// A failed job stays failed: late duplicates and sweeps are no-ops.
	calls := h.media.transcodeCalls()
	h.makeDue(v)
	h.process(w, v)
	_ = w.Sweep(context.Background())
	if h.media.transcodeCalls() != calls || h.job(v).State != "failed" {
		t.Fatal("a failed job must never run again")
	}
}

func TestStorageFailuresAreRetriedAndLeaveNoDuplicateOutputs(t *testing.T) {
	cases := map[string]string{
		"downloading the source": integrations.OpGet,
		"uploading the output":   integrations.OpPut,
		"clearing old output":    integrations.OpDelete,
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			v := h.newVideo("private")
			w := h.newWorker("w")

			// A previous crashed attempt left stale output behind.
			stale := integrations.HLSKey(v.id, "360p/seg_00099.ts")
			h.app.storage.PutData(stale, "video/mp2t", []byte{0x47})

			h.app.storage.FailWith(op, errors.New("storage is unavailable"))
			h.process(w, v)
			if sn := h.job(v); sn.State != "retry" {
				t.Fatalf("a storage failure should schedule a retry: %+v", sn)
			}
			if got := videoStateOf(t, v.id); got.status != "queued" {
				t.Fatalf("video = %+v", got)
			}

			h.app.storage.FailWith(op, nil)
			h.makeDue(v)
			h.process(w, v)
			if got := videoStateOf(t, v.id); got.status != "ready" {
				t.Fatalf("video after recovery = %+v", got)
			}
			if h.app.storage.Has(stale) {
				t.Error("stale output from the earlier attempt should have been removed")
			}
			if n := len(outputKeys(h, v)); n != 8 {
				t.Errorf("stored output objects = %d, want exactly 8: %v", n, outputKeys(h, v))
			}
			if left := h.leftoverTempDirs(); len(left) != 0 {
				t.Errorf("temp dirs left behind: %v", left)
			}
		})
	}
}

func TestReprocessingTheSameVideoOverwritesInsteadOfDuplicating(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	w := h.newWorker("w")
	h.process(w, v)
	first := outputKeys(h, v)

	// Force a second run of the same job (as if the success was never recorded).
	execSQL(t, `UPDATE processing_jobs SET state = 'queued', attempts = 0, finished_at = NULL, next_run_at = now() - interval '1 second' WHERE id = $1`, v.jobID)
	h.process(w, v)

	if second := outputKeys(h, v); strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("outputs changed between runs:\n%v\n%v", first, second)
	}
	if n := countOf(t, "video_renditions"); n != 2 {
		t.Errorf("renditions = %d, want 2 (replaced, not appended)", n)
	}
}

// ---- crashes, duplicates, concurrency ---------------------------------------

func TestCrashedWorkersJobIsRecoveredAfterItsLeaseExpires(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")

	// A worker claims the job and then dies without recording anything.
	if _, ok, err := h.store.Claim(context.Background(), v.jobID, "dead-worker", time.Minute); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if got := videoStateOf(t, v.id); got.status != "processing" {
		t.Fatalf("video = %+v", got)
	}

	// While the dead worker's lease is live nobody else may take the job.
	w := h.newWorker("survivor")
	h.process(w, v)
	if h.media.transcodeCalls() != 0 {
		t.Fatal("a job with a live lease must not be taken over")
	}
	published := len(h.app.queue.Published())
	_ = w.Sweep(context.Background())
	if len(h.app.queue.Published()) != published {
		t.Error("the sweeper must not resend a job whose lease is still valid")
	}

	// The lease expires: the sweeper resends it and another worker finishes it.
	h.expireLease(v)
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.app.queue.Published()) != published+1 {
		t.Fatalf("the abandoned job was not resent: %v", h.app.queue.Published())
	}
	h.process(w, v)

	if got := videoStateOf(t, v.id); got.status != "ready" {
		t.Fatalf("video = %+v", got)
	}
	if sn := h.job(v); sn.State != "succeeded" || sn.Attempts != 2 {
		t.Fatalf("the takeover counts as a second attempt: %+v", sn)
	}
}

func TestWorkerThatDiesOnTheLastAttemptFailsTheJob(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	execSQL(t, `UPDATE processing_jobs SET max_attempts = 2 WHERE id = $1`, v.jobID)

	for i := 0; i < 2; i++ {
		if _, ok, err := h.store.Claim(context.Background(), v.jobID, "doomed", time.Minute); err != nil || !ok {
			t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
		}
		h.expireLease(v)
	}
	if err := h.newWorker("sweeper").Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}

	if sn := h.job(v); sn.State != "failed" {
		t.Fatalf("job = %+v", sn)
	}
	if got := videoStateOf(t, v.id); got.status != "failed" || got.errMsg == nil {
		t.Fatalf("video = %+v", got)
	}
}

func TestDuplicateMessagesDoNotReprocess(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	w := h.newWorker("w")
	for i := 0; i < 3; i++ { // the same job delivered again and again
		_ = h.app.queue.Publish(context.Background(), v.jobID)
	}

	h.process(w, v)
	calls := h.media.transcodeCalls()
	for i := 0; i < 3; i++ {
		h.process(w, v)
	}
	if h.media.transcodeCalls() != calls {
		t.Fatalf("duplicate messages caused reprocessing (%d -> %d transcodes)", calls, h.media.transcodeCalls())
	}
	if got := videoStateOf(t, v.id); got.status != "ready" || h.job(v).Attempts != 1 {
		t.Fatalf("video=%+v job=%+v", got, h.job(v))
	}
	if n := countOf(t, "processing_jobs"); n != 1 {
		t.Errorf("jobs = %d", n)
	}
}

func TestMalformedMessagesAreDropped(t *testing.T) {
	h := newHarness(t)
	w := h.newWorker("w")
	for _, id := range []string{"", "not-a-uuid", "'; DROP TABLE videos; --", missingUUID} {
		if err := w.ProcessJob(context.Background(), id); err != nil {
			t.Errorf("ProcessJob(%q) = %v; garbage and unknown IDs should be dropped quietly", id, err)
		}
	}
}

func TestOnlyOneOfManyConcurrentClaimsWins(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")

	const claimers = 12
	var wins, errs int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := h.store.Claim(context.Background(), v.jobID, "claimer-"+string(rune('a'+i)), time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
			}
			if ok {
				wins++
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 || errs != 0 {
		t.Fatalf("wins=%d errors=%d, want exactly one winner", wins, errs)
	}
	if sn := h.job(v); sn.State != "processing" || sn.Attempts != 1 {
		t.Fatalf("job = %+v", sn)
	}
}

func TestConcurrentWorkersProcessAJobExactlyOnce(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := h.newWorker("worker-" + string(rune('a'+i)))
			if err := w.ProcessJob(context.Background(), v.jobID); err != nil {
				t.Errorf("worker %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if got := h.media.transcodeCalls(); got != 2 { // one pass over the two renditions of a 720p source
		t.Errorf("transcodes = %d, want 2: the job ran more than once", got)
	}
	if sn := h.job(v); sn.State != "succeeded" || sn.Attempts != 1 {
		t.Fatalf("job = %+v", sn)
	}
	if got := videoStateOf(t, v.id); got.status != "ready" {
		t.Fatalf("video = %+v", got)
	}
	if n := countOf(t, "video_renditions"); n != 2 {
		t.Errorf("renditions = %d, want 2", n)
	}
}

func TestWorkerAbandonsWorkWhenItsLeaseIsStolen(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	// Short lease so the heartbeat (every lease/3) notices quickly.
	w := h.newWorker("slow", func(c *config.WorkerConfig) { c.Lease = 600 * time.Millisecond })

	h.media.set(func(f *fakeMedia) {
		f.transcodeHook = func(ctx context.Context, req transcode.Request) error {
			// Another worker takes over (as it would after this one stalled).
			execSQL(t, `UPDATE processing_jobs SET locked_by = 'thief', lease_expires_at = now() + interval '1 hour' WHERE id = $1`, v.jobID)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("the worker was never told it lost the lease")
			}
		}
	})
	h.process(w, v)

	sn := h.job(v)
	if sn.State != "processing" || sn.LockedBy == nil || *sn.LockedBy != "thief" {
		t.Fatalf("the losing worker must not touch the job: %+v", sn)
	}
	if got := videoStateOf(t, v.id); got.status != "processing" || got.errMsg != nil {
		t.Fatalf("video = %+v", got)
	}
}

// ---- the long-running Run loop ----------------------------------------------

func runWorker(t *testing.T, w *worker.Worker) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- w.Run(ctx) }()
	t.Cleanup(cancelCtx)
	return cancelCtx, result
}

func TestRunLoopProcessesQueuedJobsAndAcknowledgesThem(t *testing.T) {
	h := newHarness(t)
	v1, v2 := h.newVideo("private"), h.newVideo("private")

	cancel, done := runWorker(t, h.newWorker("looper"))
	waitFor(t, "both videos to become ready", func() bool {
		return videoStateOf(t, v1.id).status == "ready" && videoStateOf(t, v2.id).status == "ready"
	})
	waitFor(t, "the queue to drain", func() bool {
		ready, inflight := h.app.queue.Len()
		return ready == 0 && inflight == 0
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunLoopPicksUpJobsPublishedBeforeItStarted(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	// Simulate Redis having lost everything while the worker was down.
	for {
		d, _ := h.app.queue.Receive(context.Background(), time.Millisecond)
		if d == nil {
			break
		}
		_ = h.app.queue.Ack(context.Background(), d)
	}
	execSQL(t, `UPDATE processing_jobs SET last_published_at = now() - interval '1 hour' WHERE id = $1`, v.jobID)

	runWorker(t, h.newWorker("late"))
	waitFor(t, "the sweeper to rediscover the job", func() bool { return videoStateOf(t, v.id).status == "ready" })
}

func TestGracefulShutdownLetsRunningJobsFinish(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	started, release := make(chan struct{}), make(chan struct{})
	h.media.set(func(f *fakeMedia) {
		var once sync.Once
		f.transcodeHook = func(ctx context.Context, req transcode.Request) error {
			once.Do(func() { close(started) })
			<-release
			return nil
		}
	})

	cancel, done := runWorker(t, h.newWorker("graceful"))
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the job never started")
	}
	cancel() // SIGTERM: stop taking work, but let this job finish within the grace period
	select {
	case <-done:
		t.Fatal("Run returned while a job was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the job finished")
	}
	if got := videoStateOf(t, v.id); got.status != "ready" {
		t.Fatalf("the in-flight job should have completed: %+v", got)
	}
}

func TestShutdownPastTheGracePeriodHandsTheJobBackWithoutCountingTheAttempt(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	started := make(chan struct{})
	h.media.set(func(f *fakeMedia) {
		var once sync.Once
		f.transcodeHook = func(ctx context.Context, req transcode.Request) error {
			once.Do(func() { close(started) })
			<-ctx.Done() // a transcode that never finishes on its own
			return ctx.Err()
		}
	})

	cancel, done := runWorker(t, h.newWorker("impatient", func(c *config.WorkerConfig) { c.ShutdownGrace = 100 * time.Millisecond }))
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the grace period")
	}

	sn := h.job(v)
	if sn.State != "queued" || sn.Attempts != 0 || sn.LockedBy != nil {
		t.Fatalf("an interrupted job should be released without using up an attempt: %+v", sn)
	}
	if got := videoStateOf(t, v.id); got.status != "queued" || got.errMsg != nil {
		t.Fatalf("video = %+v", got)
	}

	// A fresh worker finishes it.
	h.media.set(func(f *fakeMedia) { f.transcodeHook = nil })
	h.process(h.newWorker("next"), v)
	if got := videoStateOf(t, v.id); got.status != "ready" {
		t.Fatalf("video = %+v", got)
	}
}

// The message is acknowledged only after the result is stored. If PostgreSQL becomes
// unreachable at the moment the work finishes, the message must stay in flight and the
// job must be recoverable by another worker.
func TestMessageIsNotAcknowledgedUntilTheResultIsPersisted(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")

	// This worker gets its own database pool, which the media hook closes mid-job.
	workerPool, err := pgxpool.New(context.Background(), testConfig.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(workerPool.Close)
	started := make(chan struct{})
	h.media.set(func(f *fakeMedia) {
		var once sync.Once
		f.transcodeHook = func(ctx context.Context, req transcode.Request) error {
			once.Do(func() {
				workerPool.Close()
				close(started)
			})
			return nil
		}
	})
	w := worker.New(worker.Deps{
		Store: jobs.NewStore(workerPool), Storage: h.app.storage, Queue: h.app.queue, Media: h.media,
		Config: h.config(), Logger: discardLogger(), WorkerID: "db-loses-connection",
	})

	cancel, done := runWorker(t, w)
	<-started
	time.Sleep(500 * time.Millisecond) // let the worker try (and fail) to persist
	cancel()
	<-done

	if _, inflight := h.app.queue.Len(); inflight != 1 {
		t.Fatalf("in-flight messages = %d; an unpersisted result must leave the message unacknowledged", inflight)
	}
	if got := videoStateOf(t, v.id); got.status != "processing" {
		t.Fatalf("nothing should have been recorded: %+v", got)
	}

	// Recovery: the lease expires, a healthy worker's sweep requeues the job (dropping the
	// stale in-flight copy) and it completes.
	h.media.set(func(f *fakeMedia) { f.transcodeHook = nil })
	h.expireLease(v)
	healthy := h.newWorker("healthy")
	if err := healthy.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.process(healthy, v)
	if got := videoStateOf(t, v.id); got.status != "ready" {
		t.Fatalf("video = %+v", got)
	}
}

// ---- media delivery ----------------------------------------------------------

func apiGet(t *testing.T, b *browser, path string) response {
	t.Helper()
	return b.get("/api/v1/videos/" + path)
}

func TestHLSPlaybackIsServedFromPrivateStorageWithAuthorization(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.process(h.newWorker("w"), v)
	anon := h.app.browser()
	other := h.app.signedIn("bob@example.com")

	// Owner: playlists and segments with the right types.
	master := apiGet(t, h.owner, v.id+"/hls/master.m3u8")
	expectStatus(t, master, http.StatusOK)
	if master.header.Get("Content-Type") != "application/vnd.apple.mpegurl" || !strings.HasPrefix(string(master.body), "#EXTM3U") {
		t.Fatalf("master: %s %q", master.header.Get("Content-Type"), master.body)
	}
	seg := apiGet(t, h.owner, v.id+"/hls/720p/seg_00001.ts")
	expectStatus(t, seg, http.StatusOK)
	if seg.header.Get("Content-Type") != "video/mp2t" || len(seg.body) != 1000 || seg.body[0] != 0x47 || seg.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("segment: %s, %d bytes", seg.header.Get("Content-Type"), len(seg.body))
	}
	thumb := apiGet(t, h.owner, v.id+"/thumbnail")
	expectStatus(t, thumb, http.StatusOK)
	if thumb.header.Get("Content-Type") != "image/jpeg" || thumb.body[0] != 0xff {
		t.Fatalf("thumbnail: %s", thumb.header.Get("Content-Type"))
	}

	// A private video is invisible to everyone else, media included.
	for name, b := range map[string]*browser{"anonymous": anon, "other user": other} {
		for _, path := range []string{"/hls/master.m3u8", "/hls/360p/index.m3u8", "/thumbnail"} {
			if resp := apiGet(t, b, v.id+path); resp.status != http.StatusNotFound {
				t.Errorf("%s fetched %s: got %d", name, path, resp.status)
			}
		}
	}

	// Once public, anyone may watch, but still only through the API.
	execSQL(t, `UPDATE videos SET visibility = 'public' WHERE id = $1`, v.id)
	for _, path := range []string{"/hls/master.m3u8", "/hls/360p/index.m3u8", "/hls/360p/seg_00000.ts", "/thumbnail"} {
		if resp := apiGet(t, anon, v.id+path); resp.status != http.StatusOK {
			t.Errorf("anonymous %s: got %d", path, resp.status)
		}
	}
}

func TestHLSEndpointOnlyServesWhitelistedPathsOfReadyVideos(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("public")

	// Not ready yet: nothing is served, even to the owner.
	expectStatus(t, apiGet(t, h.owner, v.id+"/hls/master.m3u8"), http.StatusNotFound)
	expectStatus(t, apiGet(t, h.owner, v.id+"/thumbnail"), http.StatusNotFound)

	h.process(h.newWorker("w"), v)
	anon := h.app.browser()
	for _, bad := range []string{
		"/hls/360p/seg_1.ts", "/hls/360p/index.m3u8.bak", "/hls/other/index.m3u8", "/hls/master.m3u8x",
		"/hls/360p/evil.ts", "/hls/360p/", "/hls/", "/hls/720p/seg_0000a.ts", "/hls/..%2Fsource",
	} {
		if resp := apiGet(t, anon, v.id+bad); resp.status != http.StatusNotFound && resp.status != http.StatusMovedPermanently {
			t.Errorf("GET %s: got %d, want 404", bad, resp.status)
		}
	}
	// The original upload is never reachable.
	if resp := apiGet(t, anon, v.id+"/hls/source"); resp.status != http.StatusNotFound {
		t.Errorf("source via hls path: %d", resp.status)
	}
	expectStatus(t, apiGet(t, anon, missingUUID+"/hls/master.m3u8"), http.StatusNotFound)
}

func TestMediaEndpointsReportStorageProblems(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("public")
	h.process(h.newWorker("w"), v)

	h.app.storage.FailWith(integrations.OpGet, errors.New("secret internal detail"))
	resp := apiGet(t, h.app.browser(), v.id+"/hls/master.m3u8")
	if resp.status != http.StatusBadGateway || resp.errorCode(t) != "storage_unavailable" || strings.Contains(string(resp.body), "secret") {
		t.Fatalf("got %d %s", resp.status, resp.body)
	}

	h.app.storage.FailWith(integrations.OpGet, nil)
	_ = h.app.storage.DeleteObject(context.Background(), integrations.HLSMasterKey(v.id))
	expectStatus(t, apiGet(t, h.app.browser(), v.id+"/hls/master.m3u8"), http.StatusNotFound)
}

func TestDeletingAVideoRemovesItsProcessedOutput(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.process(h.newWorker("w"), v)
	if len(outputKeys(h, v)) == 0 {
		t.Fatal("expected outputs")
	}

	expectStatus(t, h.owner.del("/api/v1/videos/"+v.id), http.StatusNoContent)
	if keys := outputKeys(h, v); len(keys) != 0 {
		t.Errorf("processed output remains after deletion: %v", keys)
	}
	if h.app.storage.Has(v.sourceKey) {
		t.Error("the original should be deleted too")
	}
	if countOf(t, "processing_jobs") != 0 || countOf(t, "video_renditions") != 0 {
		t.Error("job and rendition rows should cascade with the video")
	}
}

func TestDeletingAVideoMidProcessingMakesTheWorkerBackOff(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	h.media.set(func(f *fakeMedia) {
		// Transcode runs once per rendition (360p and 720p for this probe), so delete only on the
		// first call; a second DELETE would correctly answer 404.
		var once sync.Once
		f.transcodeHook = func(ctx context.Context, req transcode.Request) error {
			once.Do(func() {
				expectStatus(t, h.owner.del("/api/v1/videos/"+v.id), http.StatusNoContent)
			})
			return nil
		}
	})
	// The worker finishes its work, finds its job gone (lease lost) and records nothing.
	h.process(h.newWorker("w"), v)
	if countOf(t, "videos") != 0 || countOf(t, "processing_jobs") != 0 {
		t.Fatal("the deleted video must stay deleted")
	}
}

// ---- database consistency ----------------------------------------------------

func TestProcessingJobConstraints(t *testing.T) {
	h := newHarness(t)
	v := h.newVideo("private")
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		_, err := testPool.Exec(ctx, sql, args...)
		return pgCode(err)
	}

	cases := []struct{ name, code string }{
		{"unknown state", exec(`UPDATE processing_jobs SET state = 'bogus' WHERE id = $1`, v.jobID)},
		{"processing without an owner", exec(`UPDATE processing_jobs SET state = 'processing' WHERE id = $1`, v.jobID)},
		{"progress above 100", exec(`UPDATE processing_jobs SET progress = 101 WHERE id = $1`, v.jobID)},
		{"more attempts than allowed", exec(`UPDATE processing_jobs SET attempts = max_attempts + 1 WHERE id = $1`, v.jobID)},
		{"zero max attempts", exec(`UPDATE processing_jobs SET max_attempts = 0 WHERE id = $1`, v.jobID)},
		{"a second job for the same video", exec(`INSERT INTO processing_jobs (video_id, upload_id, source_object_key) VALUES ($1, $2, 'k')`, v.id, v.uploadID)},
		{"unknown video status", exec(`UPDATE videos SET status = 'transcoding' WHERE id = $1`, v.id)},
		{"rendition with zero segments", exec(`INSERT INTO video_renditions (video_id, name, width, height, bandwidth_bps, segment_count, size_bytes, playlist_key) VALUES ($1, '360p', 640, 360, 1, 0, 1, 'k')`, v.id)},
		{"a valid processing job", exec(`UPDATE processing_jobs SET state = 'processing', locked_by = 'w', lease_expires_at = now() + interval '1 minute' WHERE id = $1`, v.jobID)},
	}
	for _, tc := range cases {
		want := "23514" // check_violation
		switch tc.name {
		case "a second job for the same video":
			want = "23505" // unique_violation
		case "a valid processing job":
			want = ""
		}
		if tc.code != want {
			t.Errorf("%s: SQLSTATE %q, want %q", tc.name, tc.code, want)
		}
	}
}

func TestMigrationBackfillsPhase2VideosWithoutLosingData(t *testing.T) {
	truncateTables(t)
	ctx := context.Background()
	userID := insertUser(t, "legacy@example.com")
	channelID := insertChannel(t, userID)

	// Roll back to the Phase 2 schema and create the state Phase 2 left behind: a verified
	// upload whose video sits in "processing" with nothing to process it.
	if err := database.Migrate(ctx, testConfig.DatabaseURL, "down"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Migrate(ctx, testConfig.DatabaseURL, "up")
		testPool.Reset()
	})
	testPool.Reset()

	var videoID, uploadID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO videos (channel_id, title, status, source_object_key) VALUES ($1, 'Legacy upload', 'processing', 'uploads/legacy/source') RETURNING id`, channelID).Scan(&videoID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO video_uploads (video_id, user_id, object_key, original_filename, content_type, declared_size_bytes, status, verified_size_bytes, completed_at, expires_at)
		 VALUES ($1, $2, 'uploads/legacy/source', 'a.mp4', 'video/mp4', 10, 'uploaded', 10, now(), now() + interval '1 hour') RETURNING id`, videoID, userID).Scan(&uploadID); err != nil {
		t.Fatal(err)
	}
	var draftID string
	if err := testPool.QueryRow(ctx, `INSERT INTO videos (channel_id, title) VALUES ($1, 'A draft') RETURNING id`, channelID).Scan(&draftID); err != nil {
		t.Fatal(err)
	}

	if err := database.Migrate(ctx, testConfig.DatabaseURL, "up"); err != nil {
		t.Fatal(err)
	}
	testPool.Reset()

	var status, jobState, sourceKey string
	if err := testPool.QueryRow(ctx,
		`SELECT v.status, j.state, j.source_object_key FROM videos v JOIN processing_jobs j ON j.video_id = v.id WHERE v.id = $1`, videoID).Scan(&status, &jobState, &sourceKey); err != nil {
		t.Fatalf("the legacy video was not given a job: %v", err)
	}
	if status != "queued" || jobState != "queued" || sourceKey != "uploads/legacy/source" {
		t.Fatalf("status=%s job=%s key=%s", status, jobState, sourceKey)
	}
	var draftStatus string
	_ = testPool.QueryRow(ctx, `SELECT status FROM videos WHERE id = $1`, draftID).Scan(&draftStatus)
	if draftStatus != "draft" || countOf(t, "processing_jobs") != 1 {
		t.Errorf("unrelated videos must be untouched (draft=%s, jobs=%d)", draftStatus, countOf(t, "processing_jobs"))
	}
}
