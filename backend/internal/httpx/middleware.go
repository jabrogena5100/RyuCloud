package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

type requestIDKey struct{}

// RequestID returns the ID assigned to the current request by Middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// statusRecorder remembers the status code written, for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

var unsafeMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// OriginTrusted is CSRF defence in depth on top of SameSite=Lax cookies.
//
// Browsers send an Origin header on cross-origin (and most same-origin) unsafe
// requests; requests from origins outside trusted are rejected. Requests with no
// Origin (curl, server-to-server) carry no ambient browser credentials and are
// allowed, unless Sec-Fetch-Site says the browser considers them cross-site.
func OriginTrusted(r *http.Request, trusted []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	origin = strings.TrimRight(origin, "/")
	for _, t := range trusted {
		if origin == t {
			return true
		}
	}
	// Same-origin requests, e.g. from the Swagger UI served by this API.
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && u.Host == r.Host
}

// Middleware assigns a request ID, enforces the origin check, recovers panics
// into the error envelope, and writes one structured log line per request.
func Middleware(logger *slog.Logger, trustedOrigins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w.Header().Set("X-Request-ID", id)
		rec := &statusRecorder{ResponseWriter: w}

		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				logger.Error("panic", "request_id", id, "panic", p, "stack", string(debug.Stack()))
				if rec.status == 0 {
					WriteError(rec, r, NewError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred."))
				}
			}
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("request completed",
				"request_id", id,
				"method", r.Method,
				"path", r.URL.Path, // query strings are omitted on purpose
				"status", status,
				"duration_ms", float64(time.Since(started).Microseconds())/1000,
			)
		}()

		if unsafeMethods[r.Method] && !OriginTrusted(r, trustedOrigins) {
			WriteError(rec, r, NewError(http.StatusForbidden, "untrusted_origin", "Cross-origin request rejected."))
			return
		}
		next.ServeHTTP(rec, r)
	})
}

// probeWriter captures only the status and headers of a response.
type probeWriter struct {
	header http.Header
	status int
}

func (p *probeWriter) Header() http.Header { return p.header }

func (p *probeWriter) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}
	return len(b), nil
}

func (p *probeWriter) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}

// JSONFallback replaces the mux's built-in plain-text 404 and 405 responses
// with the JSON error envelope.
func JSONFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		probe := &probeWriter{header: http.Header{}}
		h.ServeHTTP(probe, r)
		switch probe.status {
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", probe.header.Get("Allow"))
			WriteError(w, r, NewError(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed."))
		case http.StatusNotFound:
			WriteError(w, r, NewError(http.StatusNotFound, "not_found", "Not found."))
		default:
			mux.ServeHTTP(w, r)
		}
	})
}
