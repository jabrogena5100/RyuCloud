package transcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests run the real ffprobe and ffmpeg binaries (present in the worker image;
// run them with `docker compose run --rm worker go test ./internal/transcode`). They
// skip when the binaries are missing. Fixtures are synthetic: ffmpeg's built-in
// "testsrc2" picture and "sine" tone generators, created fresh for every run, so
// there is no third-party media and no licensing question.

func requireTools(t *testing.T) *Tool {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found; run this test inside the worker container", bin)
		}
	}
	return &Tool{FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", Threads: 2}
}

// makeFixture writes a short synthetic MP4 and returns its path.
func makeFixture(t *testing.T, width, height int, seconds int, withAudio bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=" + itoa(width) + "x" + itoa(height) + ":rate=24"}
	if withAudio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000")
	}
	args = append(args, "-t", itoa(seconds), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	if withAudio {
		args = append(args, "-c:a", "aac")
	}
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("could not create the fixture: %v\n%s", err, out)
	}
	return path
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRealProbe(t *testing.T) {
	tool := requireTools(t)
	src := makeFixture(t, 1280, 720, 4, true)

	p, err := tool.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 1280 || p.Height != 720 || !p.HasAudio || p.VideoCodec != "h264" || p.DurationSeconds < 3.5 || p.DurationSeconds > 4.5 {
		t.Fatalf("unexpected probe: %+v", p)
	}
	if err := p.Validate(time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestRealProbeRejectsGarbageAndUnsupportedFiles(t *testing.T) {
	tool := requireTools(t)
	dir := t.TempDir()
	cases := map[string][]byte{
		"random bytes": []byte(strings.Repeat("this is definitely not a video\n", 200)),
		"empty file":   {},
		"truncated":    {0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'},
	}
	for name, data := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".mp4")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := tool.Probe(context.Background(), path)
		if !IsInvalidMedia(err) {
			t.Errorf("%s: error = %v, want invalid media", name, err)
		}
	}
}

func TestRealTranscodeProducesHLSRenditions(t *testing.T) {
	tool := requireTools(t)
	src := makeFixture(t, 1280, 720, 8, true)
	probe, err := tool.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	var lastProgress float64
	for _, r := range Plan(probe.Width, probe.Height) {
		out := filepath.Join(t.TempDir(), r.Name)
		err := tool.Transcode(context.Background(), Request{
			Source: src, OutDir: out, Rendition: r, Duration: probe.DurationSeconds,
			OnProgress: func(f float64) { lastProgress = f },
		})
		if err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}

		text, err := os.ReadFile(filepath.Join(out, "index.m3u8"))
		if err != nil {
			t.Fatal(err)
		}
		segs, ended := ParseMediaPlaylist(string(text))
		if !ended || len(segs) < 1 {
			t.Fatalf("%s: incomplete playlist:\n%s", r.Name, text)
		}
		for _, s := range segs {
			info, err := os.Stat(filepath.Join(out, s.Name))
			if err != nil || info.Size() == 0 {
				t.Fatalf("%s: segment %s missing or empty: %v", r.Name, s.Name, err)
			}
			data, _ := os.ReadFile(filepath.Join(out, s.Name))
			if data[0] != 0x47 { // MPEG-TS sync byte
				t.Errorf("%s: segment %s is not MPEG-TS", r.Name, s.Name)
			}
		}

		// The output really is H.264 + AAC at the planned height.
		rendered, err := tool.Probe(context.Background(), filepath.Join(out, "index.m3u8"))
		if err != nil {
			t.Fatalf("%s: output does not probe: %v", r.Name, err)
		}
		if rendered.Height != r.Height || rendered.VideoCodec != "h264" || !rendered.HasAudio {
			t.Errorf("%s: output probe = %+v", r.Name, rendered)
		}
	}
	if lastProgress < 0.9 {
		t.Errorf("progress only reached %.2f", lastProgress)
	}
}

func TestRealTranscodeVideoOnlySource(t *testing.T) {
	tool := requireTools(t)
	src := makeFixture(t, 640, 360, 3, false)
	probe, err := tool.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if probe.HasAudio {
		t.Fatal("fixture should have no audio")
	}
	r := Plan(probe.Width, probe.Height)[0]
	out := filepath.Join(t.TempDir(), r.Name)
	if err := tool.Transcode(context.Background(), Request{Source: src, OutDir: out, Rendition: r, Duration: probe.DurationSeconds}); err != nil {
		t.Fatalf("a source without audio must still transcode: %v", err)
	}
}

func TestRealTranscodeDoesNotUpscaleSmallSources(t *testing.T) {
	tool := requireTools(t)
	src := makeFixture(t, 320, 240, 3, true)
	probe, _ := tool.Probe(context.Background(), src)
	plan := Plan(probe.Width, probe.Height)
	if len(plan) != 1 || plan[0].Height != 240 {
		t.Fatalf("plan = %+v", plan)
	}
	out := filepath.Join(t.TempDir(), plan[0].Name)
	if err := tool.Transcode(context.Background(), Request{Source: src, OutDir: out, Rendition: plan[0], Duration: probe.DurationSeconds}); err != nil {
		t.Fatal(err)
	}
	rendered, err := tool.Probe(context.Background(), filepath.Join(out, "index.m3u8"))
	if err != nil || rendered.Height != 240 || rendered.Width != 320 {
		t.Fatalf("output = %+v, %v", rendered, err)
	}
}

func TestRealThumbnail(t *testing.T) {
	tool := requireTools(t)
	src := makeFixture(t, 1280, 720, 4, true)
	dst := filepath.Join(t.TempDir(), "thumb.jpg")

	if err := tool.Thumbnail(context.Background(), src, dst, ThumbnailTime(4)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || len(data) < 100 || data[0] != 0xff || data[1] != 0xd8 {
		t.Fatalf("not a JPEG (%d bytes, err %v)", len(data), err)
	}
	probe, err := tool.Probe(context.Background(), dst)
	if err != nil || probe.Width != 640 {
		t.Fatalf("thumbnail should be 640 px wide: %+v, %v", probe, err)
	}

	// A position past the end fails cleanly instead of leaving an empty file.
	if err := tool.Thumbnail(context.Background(), src, filepath.Join(t.TempDir(), "late.jpg"), 3600); err == nil {
		t.Error("expected an error for a position beyond the end")
	}
}

func TestRealTranscodeHonoursCancellation(t *testing.T) {
	tool := requireTools(t)
	src := makeFixture(t, 1280, 720, 8, true)
	probe, _ := tool.Probe(context.Background(), src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Plan(probe.Width, probe.Height)[0]
	err := tool.Transcode(ctx, Request{Source: src, OutDir: filepath.Join(t.TempDir(), "x"), Rendition: r, Duration: probe.DurationSeconds})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
}
