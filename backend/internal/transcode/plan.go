package transcode

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Rendition is one output ladder rung.
type Rendition struct {
	Name     string // "360p"; also the directory name in storage
	Width    int
	Height   int
	MaxRateK int // video maxrate (kbit/s), bounds the bitrate of complex scenes
	BufSizeK int
}

// ladder lists the rungs in ascending order. A rung is produced only if the source is
// at least that tall, so nothing is ever upscaled.
var ladder = []struct {
	height   int
	maxRateK int
}{
	{360, 1000},
	{720, 3000},
}

// even rounds down to an even number (H.264 4:2:0 needs even dimensions).
func even(n int) int { return n &^ 1 }

// Plan chooses the renditions for a source of the given (display) size:
// 360p and/or 720p when the source is at least that tall, otherwise a single
// rendition at the source's own height. Widths keep the source aspect ratio.
func Plan(srcW, srcH int) []Rendition {
	var out []Rendition
	for _, rung := range ladder {
		if srcH >= rung.height {
			out = append(out, newRendition(rung.height, srcW, srcH, rung.maxRateK))
		}
	}
	if len(out) == 0 {
		// Smaller than every rung: keep the native size (no upscaling). Scale the rate
		// cap down with the pixel count, but never below a sane floor.
		rate := int(math.Max(300, float64(ladder[0].maxRateK)*float64(srcW*srcH)/float64(640*360)))
		out = append(out, newRendition(even(srcH), srcW, srcH, rate))
	}
	return out
}

func newRendition(height, srcW, srcH, maxRateK int) Rendition {
	width := even(int(math.Round(float64(srcW) * float64(height) / float64(srcH))))
	if width < 2 {
		width = 2
	}
	return Rendition{
		Name:     fmt.Sprintf("%dp", height),
		Width:    width,
		Height:   height,
		MaxRateK: maxRateK,
		BufSizeK: maxRateK * 2,
	}
}

// ThumbnailTime picks where to grab the poster frame: early enough for short clips,
// but a little in, to skip fade-ins and black first frames.
func ThumbnailTime(durationSeconds float64) float64 {
	return math.Max(0, math.Min(durationSeconds/2, 2))
}

// Segment is one media segment listed in a media playlist.
type Segment struct {
	Name     string
	Duration float64
}

// ParseMediaPlaylist reads the segments of an HLS media playlist and reports whether
// it is complete (has #EXT-X-ENDLIST), which ffmpeg writes only after finishing.
func ParseMediaPlaylist(text string) (segments []Segment, ended bool) {
	var pending float64
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			value := strings.TrimPrefix(line, "#EXTINF:")
			value, _, _ = strings.Cut(value, ",")
			pending, _ = strconv.ParseFloat(value, 64)
		case line == "#EXT-X-ENDLIST":
			ended = true
		case line != "" && !strings.HasPrefix(line, "#"):
			segments = append(segments, Segment{Name: line, Duration: pending})
			pending = 0
		}
	}
	return segments, ended
}

// Variant is a rendition as listed in the master playlist.
type Variant struct {
	Playlist     string // path relative to the master, e.g. "360p/index.m3u8"
	Width        int
	Height       int
	Bandwidth    int64 // peak bits per second
	AvgBandwidth int64
}

// MasterPlaylist builds the HLS master playlist, lowest bandwidth first.
func MasterPlaylist(variants []Variant) string {
	sorted := append([]Variant(nil), variants...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Bandwidth < sorted[j].Bandwidth })

	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, v := range sorted {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=%dx%d\n%s\n",
			v.Bandwidth, v.AvgBandwidth, v.Width, v.Height, v.Playlist)
	}
	return b.String()
}
