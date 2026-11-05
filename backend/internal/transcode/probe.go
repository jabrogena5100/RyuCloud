// Package transcode turns an uploaded video into HLS renditions and a thumbnail by
// running ffprobe and ffmpeg. Everything here is either a pure function (probe
// parsing, rendition planning, playlist building) or a thin, shell-free wrapper
// around the two binaries, so it can be tested without a media pipeline.
package transcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Limits on what is worth transcoding. Anything beyond them is rejected up front,
// before any CPU is spent.
const (
	MaxDimension = 8192
	minDimension = 16
)

// InvalidMediaError means the source can never be processed (corrupt, not a video,
// unsupported, too long). Retrying will not help, so the worker fails the job at once.
// Reason is safe to show to the video's owner.
type InvalidMediaError struct {
	Reason string
	Err    error // technical detail for logs; may be nil
}

func (e *InvalidMediaError) Error() string {
	if e.Err != nil {
		return "invalid media: " + e.Reason + ": " + e.Err.Error()
	}
	return "invalid media: " + e.Reason
}

func (e *InvalidMediaError) Unwrap() error { return e.Err }

// IsInvalidMedia reports whether err means the source can never be processed.
func IsInvalidMedia(err error) bool {
	var target *InvalidMediaError
	return errors.As(err, &target)
}

// Probe is what we need to know about a source file.
type Probe struct {
	Width           int // after applying rotation metadata
	Height          int
	DurationSeconds float64
	HasAudio        bool
	VideoCodec      string
	Container       string
}

type sideData struct {
	Rotation float64 `json:"rotation"`
}

type ffprobeOutput struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Duration    string `json:"duration"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		Tags         map[string]string `json:"tags"`
		SideDataList []sideData        `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

// ParseProbe interprets `ffprobe -print_format json -show_format -show_streams` output.
func ParseProbe(data []byte) (Probe, error) {
	var out ffprobeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return Probe{}, &InvalidMediaError{Reason: "the file could not be analysed", Err: err}
	}

	p := Probe{Container: out.Format.FormatName}
	foundVideo := false
	var streamDuration float64
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			if foundVideo || s.Disposition.AttachedPic == 1 { // cover art is not a video track
				continue
			}
			foundVideo = true
			p.Width, p.Height, p.VideoCodec = s.Width, s.Height, s.CodecName
			streamDuration = parseSeconds(s.Duration)
			if rotated(s.Tags["rotate"], s.SideDataList) {
				p.Width, p.Height = p.Height, p.Width
			}
		case "audio":
			p.HasAudio = true
		}
	}
	if !foundVideo {
		return Probe{}, &InvalidMediaError{Reason: "the file does not contain a video track"}
	}

	p.DurationSeconds = parseSeconds(out.Format.Duration)
	if p.DurationSeconds <= 0 {
		p.DurationSeconds = streamDuration
	}
	return p, nil
}

func parseSeconds(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return f
}

// rotated reports whether rotation metadata turns the picture by 90 or 270 degrees.
func rotated(tag string, side []sideData) bool {
	degrees := 0.0
	if v, err := strconv.ParseFloat(tag, 64); err == nil {
		degrees = v
	}
	for _, sd := range side {
		if sd.Rotation != 0 {
			degrees = sd.Rotation
		}
	}
	d := int(math.Abs(math.Round(degrees))) % 180
	return d == 90
}

// Validate rejects sources that cannot or should not be transcoded.
func (p Probe) Validate(maxDuration time.Duration) error {
	switch {
	case p.Width < minDimension || p.Height < minDimension:
		return &InvalidMediaError{Reason: "the video dimensions are too small or missing"}
	case p.Width > MaxDimension || p.Height > MaxDimension:
		return &InvalidMediaError{Reason: fmt.Sprintf("the video is larger than %d pixels", MaxDimension)}
	case p.DurationSeconds <= 0:
		return &InvalidMediaError{Reason: "the video has no readable duration"}
	case maxDuration > 0 && p.DurationSeconds > maxDuration.Seconds():
		return &InvalidMediaError{Reason: fmt.Sprintf("the video is longer than the %d-minute limit", int(maxDuration.Minutes()))}
	}
	return nil
}
