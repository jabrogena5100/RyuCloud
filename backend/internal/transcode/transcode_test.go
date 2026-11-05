package transcode

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const sampleProbe = `{
  "streams": [
    {"codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "duration": "10.0", "disposition": {"attached_pic": 0}},
    {"codec_type": "audio", "codec_name": "aac"}
  ],
  "format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "10.016000"}
}`

func TestParseProbe(t *testing.T) {
	p, err := ParseProbe([]byte(sampleProbe))
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 1920 || p.Height != 1080 || p.DurationSeconds != 10.016 || !p.HasAudio || p.VideoCodec != "h264" {
		t.Fatalf("unexpected probe: %+v", p)
	}
}

func TestParseProbeVideoOnlyAndDurationFallback(t *testing.T) {
	p, err := ParseProbe([]byte(`{"streams":[{"codec_type":"video","codec_name":"vp9","width":640,"height":360,"duration":"4.5"}],"format":{"format_name":"webm"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.HasAudio || p.DurationSeconds != 4.5 {
		t.Fatalf("unexpected probe: %+v", p)
	}
}

func TestParseProbeAppliesRotation(t *testing.T) {
	for name, stream := range map[string]string{
		"tag":       `{"codec_type":"video","width":1920,"height":1080,"tags":{"rotate":"90"}}`,
		"side data": `{"codec_type":"video","width":1920,"height":1080,"side_data_list":[{"rotation":-90}]}`,
	} {
		p, err := ParseProbe([]byte(`{"streams":[` + stream + `],"format":{"duration":"5"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if p.Width != 1080 || p.Height != 1920 {
			t.Errorf("%s: got %dx%d, want 1080x1920", name, p.Width, p.Height)
		}
	}
	p, _ := ParseProbe([]byte(`{"streams":[{"codec_type":"video","width":1920,"height":1080,"tags":{"rotate":"180"}}],"format":{"duration":"5"}}`))
	if p.Width != 1920 {
		t.Errorf("a 180 degree rotation must not swap dimensions: %+v", p)
	}
}

func TestParseProbeRejectsFilesWithoutVideo(t *testing.T) {
	cases := map[string]string{
		"audio only":     `{"streams":[{"codec_type":"audio"}],"format":{"duration":"5"}}`,
		"cover art only": `{"streams":[{"codec_type":"video","width":500,"height":500,"disposition":{"attached_pic":1}}],"format":{"duration":"5"}}`,
		"no streams":     `{"streams":[],"format":{}}`,
		"not json":       `this is not json`,
		"empty":          ``,
	}
	for name, data := range cases {
		_, err := ParseProbe([]byte(data))
		if !IsInvalidMedia(err) {
			t.Errorf("%s: error = %v, want invalid media", name, err)
		}
	}
}

func TestProbeValidate(t *testing.T) {
	good := Probe{Width: 1280, Height: 720, DurationSeconds: 60}
	if err := good.Validate(time.Hour); err != nil {
		t.Fatalf("valid probe rejected: %v", err)
	}
	cases := map[string]Probe{
		"tiny":          {Width: 4, Height: 4, DurationSeconds: 5},
		"huge":          {Width: 10000, Height: 5000, DurationSeconds: 5},
		"no duration":   {Width: 640, Height: 360},
		"too long":      {Width: 640, Height: 360, DurationSeconds: 2 * 3600},
		"missing sizes": {DurationSeconds: 5},
	}
	for name, p := range cases {
		if err := p.Validate(time.Hour); !IsInvalidMedia(err) {
			t.Errorf("%s: error = %v, want invalid media", name, err)
		}
	}
	// A zero limit disables the duration check.
	if err := (Probe{Width: 640, Height: 360, DurationSeconds: 99999}).Validate(0); err != nil {
		t.Errorf("zero max duration should disable the check: %v", err)
	}
}

func TestPlanNeverUpscales(t *testing.T) {
	cases := []struct {
		name          string
		w, h          int
		wantNames     []string
		wantLastWidth int
	}{
		{"1080p", 1920, 1080, []string{"360p", "720p"}, 1280},
		{"720p", 1280, 720, []string{"360p", "720p"}, 1280},
		{"480p", 854, 480, []string{"360p"}, 640},
		{"360p", 640, 360, []string{"360p"}, 640},
		{"vertical 1080x1920", 1080, 1920, []string{"360p", "720p"}, 404},
		{"240p stays 240p", 320, 240, []string{"240p"}, 320},
		{"odd height is rounded down to even", 321, 241, []string{"240p"}, 320},
		{"tiny", 64, 48, []string{"48p"}, 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := Plan(tc.w, tc.h)
			var names []string
			for _, r := range plan {
				names = append(names, r.Name)
				if r.Height > tc.h {
					t.Errorf("%s upscales a %dp source", r.Name, tc.h)
				}
				if r.Width%2 != 0 || r.Height%2 != 0 {
					t.Errorf("%s has odd dimensions %dx%d", r.Name, r.Width, r.Height)
				}
				if r.MaxRateK <= 0 || r.BufSizeK != r.MaxRateK*2 {
					t.Errorf("%s has bad rate limits: %+v", r.Name, r)
				}
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("renditions = %v, want %v", names, tc.wantNames)
			}
			if got := plan[len(plan)-1].Width; got != tc.wantLastWidth {
				t.Errorf("last rendition width = %d, want %d", got, tc.wantLastWidth)
			}
		})
	}
}

func TestThumbnailTime(t *testing.T) {
	cases := map[float64]float64{0: 0, 1: 0.5, 3: 1.5, 4: 2, 600: 2}
	for duration, want := range cases {
		if got := ThumbnailTime(duration); got != want {
			t.Errorf("ThumbnailTime(%v) = %v, want %v", duration, got, want)
		}
	}
}

func TestParseMediaPlaylist(t *testing.T) {
	const text = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000000,\nseg_00000.ts\n#EXTINF:2.500000,\nseg_00001.ts\n#EXT-X-ENDLIST\n"
	segs, ended := ParseMediaPlaylist(text)
	if !ended || len(segs) != 2 || segs[0].Name != "seg_00000.ts" || segs[0].Duration != 6 || segs[1].Duration != 2.5 {
		t.Fatalf("segments=%+v ended=%v", segs, ended)
	}

	// ffmpeg has not finished: no ENDLIST yet.
	if _, ended := ParseMediaPlaylist(strings.Replace(text, "#EXT-X-ENDLIST\n", "", 1)); ended {
		t.Error("a playlist without #EXT-X-ENDLIST must not count as complete")
	}
	if segs, ended := ParseMediaPlaylist(""); len(segs) != 0 || ended {
		t.Error("empty playlist")
	}
}

func TestMasterPlaylist(t *testing.T) {
	got := MasterPlaylist([]Variant{
		{Playlist: "720p/index.m3u8", Width: 1280, Height: 720, Bandwidth: 3_000_000, AvgBandwidth: 2_000_000},
		{Playlist: "360p/index.m3u8", Width: 640, Height: 360, Bandwidth: 900_000, AvgBandwidth: 700_000},
	})
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=900000,AVERAGE-BANDWIDTH=700000,RESOLUTION=640x360\n360p/index.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=3000000,AVERAGE-BANDWIDTH=2000000,RESOLUTION=1280x720\n720p/index.m3u8\n"
	if got != want {
		t.Fatalf("master playlist:\n%s\nwant:\n%s", got, want)
	}
}

func TestHLSArgsAreAListNotAShellCommand(t *testing.T) {
	// A hostile-looking path stays a single argv element: no shell ever parses it.
	hostile := "/tmp/job-1/source; rm -rf /"
	args := hlsArgs(Request{Source: hostile, OutDir: "/tmp/job-1/hls/360p", Rendition: Plan(1280, 720)[0]}, 3)

	count := 0
	for i, a := range args {
		if a == hostile {
			count++
			if args[i-1] != "-i" {
				t.Errorf("source is not the argument of -i: %v", args[i-1])
			}
		}
	}
	if count != 1 {
		t.Fatalf("source appears %d times in %v", count, args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"scale=-2:360", "libx264", "-c:a aac", "-threads 3", "-f hls", "-hls_playlist_type vod", "-map 0:a:0?"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	if !strings.HasSuffix(joined, "/tmp/job-1/hls/360p/index.m3u8") {
		t.Errorf("last argument should be the playlist path: %s", joined)
	}
}

func TestToolErrorClassification(t *testing.T) {
	exit := errors.New("exit status 1")
	if err := toolError("ffmpeg", exit, "[mov,mp4] moov atom not found\nError opening input"); !IsInvalidMedia(err) {
		t.Errorf("damaged input should be invalid media: %v", err)
	}
	if err := toolError("ffmpeg", exit, "Cannot allocate memory"); IsInvalidMedia(err) {
		t.Errorf("resource errors must stay retryable: %v", err)
	}
	if err := toolError("ffmpeg", &exec.Error{Name: "ffmpeg", Err: exec.ErrNotFound}, ""); IsInvalidMedia(err) || !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("a missing binary is an operational error: %v", err)
	}
}

func TestTailBufferKeepsOnlyTheEnd(t *testing.T) {
	b := &tailBuffer{max: 5}
	_, _ = b.Write([]byte("hello "))
	_, _ = b.Write([]byte("world"))
	if got := b.String(); got != "world" {
		t.Fatalf("tail = %q", got)
	}
}

func TestClamp01(t *testing.T) {
	if clamp01(-1) != 0 || clamp01(2) != 1 || clamp01(0.25) != 0.25 {
		t.Fatal("clamp01")
	}
}
