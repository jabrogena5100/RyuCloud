package integrations

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // CloudFront signed URLs and cookies are defined as RSA-SHA1; there is no alternative.
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

// CloudFrontSigner creates CloudFront signed URLs and signed cookies. The S3 bucket is
// private and reachable only through CloudFront (Origin Access Control); the distribution
// requires a valid signature from a trusted key group on every request, so a viewer who
// has not been authorised by this API cannot fetch media even if they know the URL.
//
// It is implemented with the standard library: CloudFront's format is a base64 variant of
// an RSA PKCS#1 v1.5 / SHA-1 signature over a small JSON policy.
type CloudFrontSigner struct {
	keyPairID string
	key       *rsa.PrivateKey
}

// NewCloudFrontSigner parses a PEM RSA private key (PKCS#1 or PKCS#8).
func NewCloudFrontSigner(keyPairID, privateKeyPEM string) (*CloudFrontSigner, error) {
	// Secret stores sometimes hand multi-line values back with literal "\n" sequences.
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, `\n`, "\n")
	block, _ := pem.Decode([]byte(strings.TrimSpace(privateKeyPEM)))
	if block == nil {
		return nil, errors.New("CLOUDFRONT_PRIVATE_KEY is not PEM encoded")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CloudFront private key: %w", err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CloudFront private key: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("CloudFront requires an RSA private key")
		}
		key = rk
	default:
		return nil, fmt.Errorf("unsupported PEM block %q for the CloudFront private key", block.Type)
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("CloudFront requires an RSA key of at least 2048 bits")
	}
	return &CloudFrontSigner{keyPairID: keyPairID, key: key}, nil
}

type cfPolicy struct {
	Statement []cfStatement `json:"Statement"`
}

type cfStatement struct {
	Resource  string      `json:"Resource"`
	Condition cfCondition `json:"Condition"`
}

type cfCondition struct {
	DateLessThan cfEpoch `json:"DateLessThan"`
}

type cfEpoch struct {
	EpochTime int64 `json:"AWS:EpochTime"`
}

// policyJSON returns the compact JSON policy CloudFront expects.
func policyJSON(resource string, expires time.Time) ([]byte, error) {
	return json.Marshal(cfPolicy{Statement: []cfStatement{{
		Resource:  resource,
		Condition: cfCondition{DateLessThan: cfEpoch{EpochTime: expires.Unix()}},
	}}})
}

// cfBase64 is base64 with CloudFront's URL-safe substitutions: + - = _ / ~
func cfBase64(b []byte) string {
	return strings.NewReplacer("+", "-", "=", "_", "/", "~").Replace(base64.StdEncoding.EncodeToString(b))
}

func (s *CloudFrontSigner) sign(policy []byte) (string, error) {
	sum := sha1.Sum(policy) //nolint:gosec // see the import comment
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA1, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign CloudFront policy: %w", err)
	}
	return cfBase64(sig), nil
}

// SignedURL returns rawURL with a canned-policy signature valid until expires. It is meant
// for a single object (a thumbnail): relative references inside a playlist would not carry
// the signature, which is why playback uses signed cookies instead.
func (s *CloudFrontSigner) SignedURL(rawURL string, expires time.Time) (string, error) {
	policy, err := policyJSON(rawURL, expires)
	if err != nil {
		return "", err
	}
	sig, err := s.sign(policy)
	if err != nil {
		return "", err
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "Expires=" + strconv.FormatInt(expires.Unix(), 10) +
		"&Signature=" + sig + "&Key-Pair-Id=" + s.keyPairID, nil
}

// SignedCookies returns the three CloudFront cookies that authorise every URL matching
// resource (which may end in a * wildcard) until expires.
func (s *CloudFrontSigner) SignedCookies(resource string, expires time.Time) (map[string]string, error) {
	policy, err := policyJSON(resource, expires)
	if err != nil {
		return nil, err
	}
	sig, err := s.sign(policy)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"CloudFront-Policy":      cfBase64(policy),
		"CloudFront-Signature":   sig,
		"CloudFront-Key-Pair-Id": s.keyPairID,
	}, nil
}

// PlaybackGrantor is implemented by playback providers that must authorise the viewer
// before the URLs they return will work. The videos handler calls it, after its own
// visibility check, whenever it returns a playable video.
type PlaybackGrantor interface {
	GrantPlayback(w http.ResponseWriter, videoID string)
}

// CloudFrontPlayback returns CDN URLs for private media.
//
//	playback:   https://{domain}/videos/{id}/hls/master.m3u8, authorised by signed cookies
//	            scoped to /videos/{id}/ (set by GrantPlayback), so segments and renditions
//	            referenced relatively from the playlist are covered too
//	thumbnails: a signed URL per image, so a list of videos does not need a cookie each
type CloudFrontPlayback struct {
	signer       *CloudFrontSigner
	domain       string
	cookieDomain string
	playbackTTL  time.Duration
	thumbnailTTL time.Duration
	now          func() time.Time
	// OnError reports signing failures (nil means ignore). Handlers cannot return an error from here.
	OnError func(videoID string, err error)
}

// NewCloudFrontPlayback builds the provider from validated configuration.
func NewCloudFrontPlayback(cfg config.CloudFrontConfig) (*CloudFrontPlayback, error) {
	signer, err := NewCloudFrontSigner(cfg.KeyPairID, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &CloudFrontPlayback{
		signer: signer, domain: cfg.Domain, cookieDomain: cfg.CookieDomain,
		playbackTTL: cfg.PlaybackTTL, thumbnailTTL: cfg.ThumbnailTTL, now: time.Now,
	}, nil
}

func (p *CloudFrontPlayback) objectURL(key string) string {
	return (&url.URL{Scheme: "https", Host: p.domain, Path: "/" + key}).String()
}

func (p *CloudFrontPlayback) PlaybackURL(videoID string, manifestKey *string) *string {
	if manifestKey == nil {
		return nil
	}
	u := p.objectURL(HLSMasterKey(videoID))
	return &u
}

func (p *CloudFrontPlayback) ThumbnailURL(videoID string, objectKey *string) *string {
	if objectKey == nil {
		return nil
	}
	signed, err := p.signer.SignedURL(p.objectURL(*objectKey), p.now().Add(p.thumbnailTTL))
	if err != nil {
		if p.OnError != nil {
			p.OnError(videoID, err)
		}
		return nil // the UI falls back to a placeholder
	}
	return &signed
}

// GrantPlayback sets the signed cookies for one video. The cookies are HttpOnly and
// Secure, scoped to the shared parent domain and to /videos/{id}/, so they are only sent
// to the media host and only for this video.
func (p *CloudFrontPlayback) GrantPlayback(w http.ResponseWriter, videoID string) {
	expires := p.now().Add(p.playbackTTL)
	cookies, err := p.signer.SignedCookies(p.objectURL(VideoPrefix(videoID)+"*"), expires)
	if err != nil {
		if p.OnError != nil {
			p.OnError(videoID, err)
		}
		return
	}
	for name, value := range cookies {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    value,
			Domain:   p.cookieDomain,
			Path:     "/" + VideoPrefix(videoID),
			Expires:  expires,
			MaxAge:   int(p.playbackTTL / time.Second),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// NewPlayback returns the playback provider selected by PLAYBACK_BACKEND.
func NewPlayback(cfg config.Config, apiPrefix string) (PlaybackURLProvider, error) {
	switch cfg.PlaybackBackend {
	case "cloudfront":
		return NewCloudFrontPlayback(cfg.CloudFront)
	case "api", "":
		return NewPlaybackProvider(apiPrefix), nil
	default:
		return nil, fmt.Errorf("unknown playback backend %q", cfg.PlaybackBackend)
	}
}
