package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/server"
)

const (
	trustedOrigin   = "http://localhost:3000"
	defaultPassword = "correct-horse-battery"
	cookieName      = "sf_session"
	missingUUID     = "00000000-0000-4000-8000-000000000000"
)

type testApp struct {
	t       *testing.T
	url     string
	storage *integrations.MemoryStorage // the object store behind the API (nil with newTestAppWithStorage)
}

// newTestApp starts the API on a local port against a freshly emptied database
// and an in-memory object store the tests can inspect and break.
func newTestApp(t *testing.T) *testApp {
	t.Helper()
	storage := integrations.NewMemoryStorage()
	app := newTestAppWithStorage(t, storage)
	app.storage = storage
	return app
}

// newTestAppWithStorage is newTestApp with a caller-supplied object store.
func newTestAppWithStorage(t *testing.T, storage integrations.ObjectStorage) *testApp {
	t.Helper()
	truncateTables(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(server.New(testConfig, testPool, logger, server.WithStorage(storage)))
	t.Cleanup(srv.Close)
	return &testApp{t: t, url: srv.URL}
}

func truncateTables(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`TRUNCATE users, auth_sessions, channels, videos, video_uploads RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func execSQL(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// browser is an HTTP client with its own cookie jar, i.e. a separate user agent.
type browser struct {
	t      *testing.T
	base   string
	client *http.Client
}

func (a *testApp) browser() *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		a.t.Fatal(err)
	}
	return &browser{t: a.t, base: a.url, client: &http.Client{Jar: jar}}
}

// signedIn returns a browser logged in as a newly registered user.
func (a *testApp) signedIn(email string) *browser {
	a.t.Helper()
	b := a.browser()
	expectStatus(a.t, b.register(email), http.StatusCreated)
	return b
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) object(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return out
}

func (r response) errorObject(t *testing.T) map[string]any {
	t.Helper()
	e, _ := r.object(t)["error"].(map[string]any)
	return e
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	code, _ := r.errorObject(t)["code"].(string)
	return code
}

// hasDetail reports whether a validation error mentions the given body field.
func (r response) hasDetail(t *testing.T, field string) bool {
	t.Helper()
	details, _ := r.errorObject(t)["details"].([]any)
	for _, d := range details {
		detail, _ := d.(map[string]any)
		loc, _ := detail["loc"].([]any)
		if len(loc) == 2 && loc[1] == field {
			return true
		}
	}
	return false
}

func (b *browser) do(method, path string, body any, headers map[string]string) response {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, b.base+path, reader)
	if err != nil {
		b.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

func (b *browser) get(path string) response { return b.do(http.MethodGet, path, nil, nil) }

func (b *browser) post(path string, body any) response { return b.do(http.MethodPost, path, body, nil) }

func (b *browser) patch(path string, body any) response { return b.do(http.MethodPatch, path, body, nil) }

func (b *browser) del(path string) response { return b.do(http.MethodDelete, path, nil, nil) }

func (b *browser) register(email string) response {
	return b.post("/api/v1/auth/register", map[string]any{
		"email":        email,
		"password":     defaultPassword,
		"display_name": strings.Split(email, "@")[0],
	})
}

func (b *browser) login(email, password string, headers map[string]string) response {
	return b.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": email, "password": password}, headers)
}

func (b *browser) sessionToken() string {
	b.t.Helper()
	u, err := url.Parse(b.base)
	if err != nil {
		b.t.Fatal(err)
	}
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

func (b *browser) createChannel(handle, name string) map[string]any {
	b.t.Helper()
	resp := b.post("/api/v1/channels", map[string]any{"handle": handle, "name": name})
	expectStatus(b.t, resp, http.StatusCreated)
	return resp.object(b.t)
}

func (b *browser) createVideo(channelID string, extra map[string]any) map[string]any {
	b.t.Helper()
	payload := map[string]any{"channel_id": channelID, "title": "Intro to S3"}
	for k, v := range extra {
		payload[k] = v
	}
	resp := b.post("/api/v1/videos", payload)
	expectStatus(b.t, resp, http.StatusCreated)
	return resp.object(b.t)
}

func expectStatus(t *testing.T, resp response, want int) {
	t.Helper()
	if resp.status != want {
		t.Fatalf("status = %d, want %d; body: %s", resp.status, want, resp.body)
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
