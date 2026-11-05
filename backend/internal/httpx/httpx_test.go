package httpx

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestOriginTrusted(t *testing.T) {
	trusted := []string{"http://localhost:3000"}
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"no origin (curl)", nil, true},
		{"cross-site fetch without origin", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"trusted origin", map[string]string{"Origin": "http://localhost:3000"}, true},
		{"same origin", map[string]string{"Origin": "http://api.test"}, true},
		{"untrusted origin", map[string]string{"Origin": "https://evil.example"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://api.test/", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := OriginTrusted(r, trusted); got != tc.want {
				t.Errorf("OriginTrusted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMiddlewareRejectsUntrustedOrigin(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler should not run") })
	h := Middleware(discardLogger(), []string{"http://localhost:3000"}, next)

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"code":"untrusted_origin"`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID header")
	}
}

func TestMiddlewareRecoversPanics(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	w := httptest.NewRecorder()
	Middleware(discardLogger(), nil, next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestJSONFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /things", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := JSONFallback(mux)

	cases := []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/things", http.StatusNoContent, ""},
		{http.MethodGet, "/missing", http.StatusNotFound, "not_found"},
		{http.MethodPost, "/things", http.StatusMethodNotAllowed, "method_not_allowed"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
		if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s %s: unexpected body %s", tc.method, tc.path, w.Body.String())
		}
	}
}

func TestFieldsCollectsEveryProblem(t *testing.T) {
	body := `{"name": null, "email": "Bob@Example.com", "count": 3, "short": "ab"}`
	f, err := DecodeObject(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Email("email"); got != "bob@example.com" {
		t.Errorf("Email = %q", got)
	}
	f.String("name", false, 1, 10)   // explicit null
	f.String("missing", true, 1, 10) // required but absent
	f.String("count", false, 0, 10)  // wrong type
	f.String("short", true, 3, 10)   // too short
	f.String("absent", false, 1, 10) // optional and absent: fine

	var apiErr *Error
	if !errors.As(f.Err(), &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("expected a 422 error, got %v", f.Err())
	}
	if len(apiErr.Details) != 4 {
		t.Errorf("got %d details, want 4: %+v", len(apiErr.Details), apiErr.Details)
	}
}

func TestDecodeObjectRejectsNonObjects(t *testing.T) {
	for _, body := range []string{"", "[]", "null", "{bad"} {
		_, err := DecodeObject(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "validation_error" {
			t.Errorf("body %q: expected validation_error, got %v", body, err)
		}
	}
}

func TestEmailValidation(t *testing.T) {
	for _, email := range []string{"not-an-email", "Alice <a@b.com>", "a@localhost", "@example.com"} {
		f, err := DecodeObject(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/",
			strings.NewReader(`{"email": "`+email+`"}`)))
		if err != nil {
			t.Fatal(err)
		}
		f.Email("email")
		if f.Err() == nil {
			t.Errorf("%q should be rejected", email)
		}
	}
}

func decodeFields(t *testing.T, body string) *Fields {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	f, err := DecodeObject(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("DecodeObject(%s): %v", body, err)
	}
	return f
}

func TestFieldsInt64(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   int64
		wantOK bool
		errTyp string // validation type expected, "" when valid
	}{
		{"valid", `{"n": 42}`, 42, true, ""},
		{"missing", `{}`, 0, false, "missing"},
		{"null", `{"n": null}`, 0, false, "null"},
		{"string", `{"n": "42"}`, 0, false, "int_type"},
		{"float", `{"n": 1.5}`, 0, false, "int_type"},
		{"too small", `{"n": 0}`, 0, false, "int_too_small"},
		{"too large", `{"n": 101}`, 0, false, "int_too_large"},
		{"beyond int64", `{"n": 99999999999999999999}`, 0, false, "int_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := decodeFields(t, tc.body)
			got, ok := f.Int64("n", true, 1, 100)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("Int64 = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.wantOK)
			}
			err := f.Err()
			if tc.errTyp == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var apiErr *Error
			if !errors.As(err, &apiErr) || len(apiErr.Details) != 1 || apiErr.Details[0].Type != tc.errTyp {
				t.Fatalf("error = %v, want one detail of type %q", err, tc.errTyp)
			}
		})
	}
}
