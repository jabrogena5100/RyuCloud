package integrations

import "net/url"

// PlaybackURLProvider turns stored object keys into URLs clients can fetch.
// Handlers only pass IDs and keys through it, so switching to CloudFront
// (including signed URLs) in Phase 4 won't change handler code.
type PlaybackURLProvider interface {
	PlaybackURL(videoID string, manifestKey *string) *string
	ThumbnailURL(videoID string, objectKey *string) *string
}

// NullPlayback never returns a URL. Used when nothing is streamable.
type NullPlayback struct{}

func (NullPlayback) PlaybackURL(string, *string) *string  { return nil }
func (NullPlayback) ThumbnailURL(string, *string) *string { return nil }

// APIPlayback is the local-development provider: media is served by the Go API
// itself (GET /videos/{id}/hls/... and /videos/{id}/thumbnail), which checks the
// same visibility rules as the video and streams from private storage. It keeps
// storage private without a CDN. Phase 4 replaces it with a CloudFront provider
// that returns signed CDN URLs, and the API stops carrying media bytes.
type APIPlayback struct {
	// Prefix is the API base path, e.g. "/api/v1".
	Prefix string
}

func (p APIPlayback) PlaybackURL(videoID string, manifestKey *string) *string {
	if manifestKey == nil {
		return nil
	}
	u := p.Prefix + "/videos/" + url.PathEscape(videoID) + "/hls/master.m3u8"
	return &u
}

func (p APIPlayback) ThumbnailURL(videoID string, objectKey *string) *string {
	if objectKey == nil {
		return nil
	}
	u := p.Prefix + "/videos/" + url.PathEscape(videoID) + "/thumbnail"
	return &u
}

// NewPlaybackProvider returns the API-served provider for the given API prefix.
func NewPlaybackProvider(apiPrefix string) PlaybackURLProvider { return APIPlayback{Prefix: apiPrefix} }
