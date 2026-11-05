package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestLiveness(t *testing.T) {
	resp := newTestApp(t).browser().get("/api/v1/health")
	expectStatus(t, resp, http.StatusOK)
	if str(resp.object(t), "status") != "ok" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
}

func TestReadinessChecksDatabase(t *testing.T) {
	resp := newTestApp(t).browser().get("/api/v1/health/ready")
	expectStatus(t, resp, http.StatusOK)
}

func TestOpenAPISpecAndDocsAreServed(t *testing.T) {
	b := newTestApp(t).browser()
	spec := b.get("/api/v1/openapi.yaml")
	expectStatus(t, spec, http.StatusOK)
	for _, path := range []string{"/health:", "/auth/login:", "/channels:", "/videos:", "/videos/{id}:"} {
		if !strings.Contains(string(spec.body), path) {
			t.Errorf("spec is missing %s", path)
		}
	}
	expectStatus(t, b.get("/api/v1/docs"), http.StatusOK)
}

func TestUnknownRouteUsesErrorEnvelope(t *testing.T) {
	resp := newTestApp(t).browser().get("/api/v1/does-not-exist")
	expectStatus(t, resp, http.StatusNotFound)
	if resp.errorCode(t) != "not_found" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
	if str(resp.errorObject(t), "request_id") != resp.header.Get("X-Request-ID") {
		t.Error("error request_id should match the X-Request-ID header")
	}
}

func TestWrongMethodReturns405(t *testing.T) {
	resp := newTestApp(t).browser().do(http.MethodPut, "/api/v1/auth/me", nil, nil)
	expectStatus(t, resp, http.StatusMethodNotAllowed)
	if resp.errorCode(t) != "method_not_allowed" || resp.header.Get("Allow") == "" {
		t.Fatalf("unexpected response: %v %s", resp.header, resp.body)
	}
}

func TestRequestIDHandling(t *testing.T) {
	b := newTestApp(t).browser()
	echoed := b.do(http.MethodGet, "/api/v1/health", nil, map[string]string{"X-Request-ID": "abc-123"})
	if got := echoed.header.Get("X-Request-ID"); got != "abc-123" {
		t.Errorf("valid request ID not echoed: %q", got)
	}
	replaced := b.do(http.MethodGet, "/api/v1/health", nil, map[string]string{"X-Request-ID": "bad id with spaces"})
	if got := replaced.header.Get("X-Request-ID"); got == "" || got == "bad id with spaces" {
		t.Errorf("invalid request ID not replaced: %q", got)
	}
}
