package worker

import (
	"testing"
	"time"
)

func TestBackoffDoublesAndCaps(t *testing.T) {
	base, max := 30*time.Second, 15*time.Minute
	// jitter 0.5 is the centre of the +/-20% window, i.e. no change.
	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second, 900 * time.Second, 900 * time.Second}
	for i, w := range want {
		if got := Backoff(i+1, base, max, 0.5); got != w {
			t.Errorf("Backoff(attempt %d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestBackoffJitterStaysWithinTwentyPercent(t *testing.T) {
	base, max := 100*time.Second, time.Hour
	if got := Backoff(1, base, max, 0); got != 80*time.Second {
		t.Errorf("lowest jitter = %v, want 80s", got)
	}
	if got := Backoff(1, base, max, 0.999); got < 119*time.Second || got > 120*time.Second {
		t.Errorf("highest jitter = %v, want ~120s", got)
	}
}

func TestBackoffNeverExceedsMax(t *testing.T) {
	if got := Backoff(30, time.Second, time.Minute, 0.999); got > time.Minute {
		t.Errorf("Backoff = %v, exceeds the cap", got)
	}
}

func TestRankOrdersMasterPlaylistLast(t *testing.T) {
	if !(rank("360p/seg_00000.ts") < rank("360p/index.m3u8") && rank("360p/index.m3u8") < rank("master.m3u8")) {
		t.Fatal("upload order must be segments, media playlists, master playlist")
	}
}

func TestContentTypes(t *testing.T) {
	cases := map[string]string{
		"master.m3u8":       "application/vnd.apple.mpegurl",
		"360p/seg_00001.ts": "video/mp2t",
		"thumbnail.jpg":     "image/jpeg",
		"weird.bin":         "application/octet-stream",
	}
	for name, want := range cases {
		if got := contentTypeFor(name); got != want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", name, got, want)
		}
	}
}
