// Package health serves liveness and readiness checks.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

type status struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	// SchemaVersion is the database's applied migration version (readiness only).
	SchemaVersion int64 `json:"schema_version,omitempty"`
}

// Handler serves /health (the process is up) and /health/ready (the database is reachable and its
// schema is at least as new as the migrations this binary expects).
type Handler struct {
	version string
	// expected is the newest migration embedded in this binary. If it cannot be determined the
	// handler refuses to report ready, rather than silently skipping the schema check.
	expected    int64
	expectedErr error
	ping        func(context.Context) error
	applied     func(context.Context) (int64, error)
}

func NewHandler(db *pgxpool.Pool, version string) *Handler {
	expected, err := database.ExpectedSchemaVersion()
	return &Handler{
		version:     version,
		expected:    expected,
		expectedErr: err,
		ping:        db.Ping,
		applied:     func(ctx context.Context) (int64, error) { return database.AppliedSchemaVersion(ctx, db) },
	}
}

func (h *Handler) Routes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/health", h.live)
	mux.Handle("GET "+prefix+"/health/ready", httpx.Handler(h.ready))
}

func (h *Handler) live(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, status{Status: "ok", Version: h.version})
}

// ready is 200 only when PostgreSQL answers and every migration this binary expects has been
// applied. A database that is NEWER than the binary (for example during a rollback of the app) is
// still ready: migrations here are additive, so older code keeps working against a newer schema.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.ping(ctx); err != nil {
		slog.WarnContext(ctx, "readiness check failed: database unreachable", "error", err)
		return httpx.NewError(http.StatusServiceUnavailable, "database_unavailable", "Database is not reachable.")
	}
	if h.expectedErr != nil {
		slog.ErrorContext(ctx, "readiness check failed: cannot determine expected schema version", "error", h.expectedErr)
		return httpx.NewError(http.StatusServiceUnavailable, "schema_unknown", "The expected database schema version is unknown.")
	}
	applied, err := h.applied(ctx)
	if err != nil {
		slog.WarnContext(ctx, "readiness check failed: cannot read schema version", "error", err)
		return httpx.NewError(http.StatusServiceUnavailable, "database_unavailable", "Database schema version could not be read.")
	}
	if applied < h.expected {
		slog.WarnContext(ctx, "readiness check failed: database migrations are pending", "applied", applied, "expected", h.expected)
		return httpx.NewError(http.StatusServiceUnavailable, "migrations_pending",
			fmt.Sprintf("Database schema is at version %d but version %d is required. Run: api migrate up.", applied, h.expected))
	}
	httpx.WriteJSON(w, http.StatusOK, status{Status: "ok", Version: h.version, SchemaVersion: applied})
	return nil
}
