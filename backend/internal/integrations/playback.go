package integrations

// PlaybackURLProvider turns stored object keys into URLs clients can fetch.
// Handlers only pass keys through it, so switching to CloudFront (including
// signed URLs) won't change handler code.
type PlaybackURLProvider interface {
	PlaybackURL(manifestKey *string) *string
	ThumbnailURL(objectKey *string) *string
}

// NullPlayback is the Phase 1 provider: nothing is streamable yet, so it never returns a URL.
type NullPlayback struct{}

func (NullPlayback) PlaybackURL(*string) *string { return nil }
func (NullPlayback) ThumbnailURL(*string) *string { return nil }

// NewPlaybackProvider returns the configured playback URL provider.
func NewPlaybackProvider() PlaybackURLProvider { return NullPlayback{} }
