package integrations

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // matches the implementation under test
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

func newTestKey(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return key, pkcs1, pkcs8
}

// cfDecode reverses CloudFront's base64 substitutions.
func cfDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(strings.NewReplacer("-", "+", "_", "=", "~", "/").Replace(s))
	if err != nil {
		t.Fatalf("not CloudFront base64: %v", err)
	}
	return b
}

func verify(t *testing.T, pub *rsa.PublicKey, policy []byte, signature string) {
	t.Helper()
	sum := sha1.Sum(policy) //nolint:gosec // see import
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], cfDecode(t, signature)); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

func TestCloudFrontSignerAcceptsPKCS1AndPKCS8(t *testing.T) {
	_, pkcs1, pkcs8 := newTestKey(t)
	for name, pemText := range map[string]string{"pkcs1": pkcs1, "pkcs8": pkcs8, "escaped newlines": strings.ReplaceAll(pkcs1, "\n", `\n`)} {
		if _, err := NewCloudFrontSigner("K1", pemText); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCloudFrontSignerRejectsBadKeys(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	smallPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(small)}))
	for name, pemText := range map[string]string{"empty": "", "garbage": "not a key", "too small": smallPEM} {
		if _, err := NewCloudFrontSigner("K1", pemText); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSignedURLIsVerifiable(t *testing.T) {
	key, pemText, _ := newTestKey(t)
	signer, err := NewCloudFrontSigner("KEY123", pemText)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Unix(1_900_000_000, 0)
	raw := "https://media.example.com/videos/v1/thumbnail.jpg"
	signed, err := signer.SignedURL(raw, expires)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("Expires") != "1900000000" || q.Get("Key-Pair-Id") != "KEY123" {
		t.Fatalf("query = %v", q)
	}
	if strings.ContainsAny(q.Get("Signature"), "+=/") {
		t.Error("signature must use CloudFront's URL-safe alphabet")
	}
	policy := `{"Statement":[{"Resource":"` + raw + `","Condition":{"DateLessThan":{"AWS:EpochTime":1900000000}}}]}`
	verify(t, &key.PublicKey, []byte(policy), q.Get("Signature"))
}

func TestSignedCookiesCarryAWildcardPolicy(t *testing.T) {
	key, pemText, _ := newTestKey(t)
	signer, _ := NewCloudFrontSigner("KEY123", pemText)
	resource := "https://media.example.com/videos/v1/*"
	cookies, err := signer.SignedCookies(resource, time.Unix(1_900_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if cookies["CloudFront-Key-Pair-Id"] != "KEY123" {
		t.Errorf("key pair id = %q", cookies["CloudFront-Key-Pair-Id"])
	}
	policy := cfDecode(t, cookies["CloudFront-Policy"])
	var parsed cfPolicy
	if err := json.Unmarshal(policy, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed.Statement[0].Resource; got != resource {
		t.Errorf("resource = %q", got)
	}
	verify(t, &key.PublicKey, policy, cookies["CloudFront-Signature"])
}

func testCloudFront(t *testing.T) *CloudFrontPlayback {
	t.Helper()
	_, pemText, _ := newTestKey(t)
	p, err := NewCloudFrontPlayback(config.CloudFrontConfig{
		Domain: "media.example.com", KeyPairID: "KEY123", PrivateKey: pemText, CookieDomain: "example.com",
		PlaybackTTL: 2 * time.Hour, ThumbnailTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return p
}

func TestCloudFrontPlaybackURLs(t *testing.T) {
	p := testCloudFront(t)
	manifest := "videos/v1/hls/master.m3u8"
	got := p.PlaybackURL("v1", &manifest)
	if got == nil || *got != "https://media.example.com/videos/v1/hls/master.m3u8" {
		t.Fatalf("PlaybackURL = %v", got)
	}
	if p.PlaybackURL("v1", nil) != nil || p.ThumbnailURL("v1", nil) != nil {
		t.Error("no manifest or thumbnail must mean no URL")
	}
	thumb := "videos/v1/thumbnail.jpg"
	signed := p.ThumbnailURL("v1", &thumb)
	if signed == nil || !strings.HasPrefix(*signed, "https://media.example.com/videos/v1/thumbnail.jpg?Expires=1800003600&") {
		t.Fatalf("ThumbnailURL = %v", signed)
	}
}

func TestGrantPlaybackSetsScopedSecureCookies(t *testing.T) {
	p := testCloudFront(t)
	rec := httptest.NewRecorder()
	p.GrantPlayback(rec, "v1")

	cookies := rec.Result().Cookies()
	if len(cookies) != 3 {
		t.Fatalf("got %d cookies, want 3", len(cookies))
	}
	names := map[string]bool{}
	for _, c := range cookies {
		names[c.Name] = true
		if c.Domain != "example.com" || c.Path != "/videos/v1/" {
			t.Errorf("%s: domain %q path %q", c.Name, c.Domain, c.Path)
		}
		if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s: Secure=%v HttpOnly=%v SameSite=%v", c.Name, c.Secure, c.HttpOnly, c.SameSite)
		}
		if c.MaxAge != 7200 {
			t.Errorf("%s: MaxAge = %d", c.Name, c.MaxAge)
		}
	}
	for _, want := range []string{"CloudFront-Policy", "CloudFront-Signature", "CloudFront-Key-Pair-Id"} {
		if !names[want] {
			t.Errorf("missing cookie %s", want)
		}
	}
}

func TestNewPlaybackSelectsBackend(t *testing.T) {
	p, err := NewPlayback(config.Config{PlaybackBackend: "api"}, "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := "k"
	if got := p.PlaybackURL("v1", &manifest); got == nil || *got != "/api/v1/videos/v1/hls/master.m3u8" {
		t.Errorf("api playback URL = %v", got)
	}
	if _, ok := p.(PlaybackGrantor); ok {
		t.Error("the API provider needs no grant")
	}
	if _, err := NewPlayback(config.Config{PlaybackBackend: "ftp"}, "/api/v1"); err == nil {
		t.Error("expected an error for an unknown backend")
	}
}
