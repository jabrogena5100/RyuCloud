package uploads

import "testing"

func TestNormalizeContentType(t *testing.T) {
	cases := map[string]string{
		"video/mp4":                   "video/mp4",
		"  Video/MP4 ":                "video/mp4",
		"video/webm; codecs=vp9,opus": "video/webm",
		"":                            "",
	}
	for in, want := range cases {
		if got := normalizeContentType(in); got != want {
			t.Errorf("normalizeContentType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContentTypeAllowed(t *testing.T) {
	for _, ok := range []string{"video/mp4", "video/quicktime", "video/webm", "video/x-matroska"} {
		if !contentTypeAllowed(ok) {
			t.Errorf("%s should be allowed", ok)
		}
	}
	for _, bad := range []string{"", "text/html", "application/pdf", "image/png", "application/octet-stream", "video/x-flv", "video/*"} {
		if contentTypeAllowed(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestExtensionMatches(t *testing.T) {
	cases := []struct {
		filename, contentType string
		want                  bool
	}{
		{"clip.mp4", "video/mp4", true},
		{"CLIP.MP4", "video/mp4", true},
		{"clip.m4v", "video/mp4", true},
		{"holiday.mov", "video/quicktime", true},
		{"clip.webm", "video/webm", true},
		{"movie.mkv", "video/x-matroska", true},
		{"malware.exe", "video/mp4", false},
		{"clip.mov", "video/mp4", false},
		{"clip", "video/mp4", false},
		{"clip.mp4.exe", "video/mp4", false},
	}
	for _, tc := range cases {
		if got := extensionMatches(tc.filename, tc.contentType); got != tc.want {
			t.Errorf("extensionMatches(%q, %q) = %v, want %v", tc.filename, tc.contentType, got, tc.want)
		}
	}
}

func TestFilenameProblem(t *testing.T) {
	for _, ok := range []string{"clip.mp4", "my holiday (2026).mov", "vidéo.webm"} {
		if p := filenameProblem(ok); p != "" {
			t.Errorf("filenameProblem(%q) = %q, want none", ok, p)
		}
	}
	for _, bad := range []string{"../etc/passwd", "a/b.mp4", `a\b.mp4`, "bad\x00.mp4", "line\nbreak.mp4"} {
		if filenameProblem(bad) == "" {
			t.Errorf("filenameProblem(%q) should report a problem", bad)
		}
	}
}

func TestAllowedLists(t *testing.T) {
	if got := AllowedContentTypes(); len(got) != 4 || got[0] != "video/mp4" {
		t.Errorf("AllowedContentTypes = %v", got)
	}
	exts := AllowedExtensions()
	if len(exts) != 5 {
		t.Errorf("AllowedExtensions = %v", exts)
	}
}
