package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

func readyResponse(t *testing.T, h *Handler) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	httpx.Handler(h.ready).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, rec.Body.String())
	}
	return rec.Code, body
}

func handlerWith(expected, applied int64, pingErr, appliedErr error) *Handler {
	return &Handler{
		version:  "test",
		expected: expected,
		ping:     func(context.Context) error { return pingErr },
		applied:  func(context.Context) (int64, error) { return applied, appliedErr },
	}
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestReadyWhenSchemaIsCurrent(t *testing.T) {
	code, body := readyResponse(t, handlerWith(3, 3, nil, nil))
	if code != http.StatusOK || body["status"] != "ok" || body["schema_version"] != float64(3) {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestReadyWhenDatabaseIsNewerThanTheBinary(t *testing.T) {
	if code, body := readyResponse(t, handlerWith(3, 5, nil, nil)); code != http.StatusOK {
		t.Fatalf("a newer schema should still be ready: %d %v", code, body)
	}
}

func TestNotReadyWhileMigrationsArePending(t *testing.T) {
	code, body := readyResponse(t, handlerWith(3, 1, nil, nil))
	if code != http.StatusServiceUnavailable || errorCode(body) != "migrations_pending" {
		t.Fatalf("got %d %v", code, body)
	}
	message := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "version 1") || !strings.Contains(message, "version 3") {
		t.Errorf("message should name both versions: %q", message)
	}
}

func TestNotReadyOnAnEmptyDatabase(t *testing.T) {
	code, body := readyResponse(t, handlerWith(3, 0, nil, nil))
	if code != http.StatusServiceUnavailable || errorCode(body) != "migrations_pending" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestNotReadyWhenDatabaseIsDown(t *testing.T) {
	code, body := readyResponse(t, handlerWith(3, 3, errors.New("connection refused"), nil))
	if code != http.StatusServiceUnavailable || errorCode(body) != "database_unavailable" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestNotReadyWhenSchemaVersionCannotBeRead(t *testing.T) {
	code, body := readyResponse(t, handlerWith(3, 0, nil, errors.New("boom")))
	if code != http.StatusServiceUnavailable || errorCode(body) != "database_unavailable" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestNotReadyWhenExpectedVersionIsUnknown(t *testing.T) {
	h := handlerWith(0, 3, nil, nil)
	h.expectedErr = errors.New("no migrations")
	code, body := readyResponse(t, h)
	if code != http.StatusServiceUnavailable || errorCode(body) != "schema_unknown" {
		t.Fatalf("got %d %v", code, body)
	}
}
