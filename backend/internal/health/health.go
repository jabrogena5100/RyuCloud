// Package health serves liveness and readiness checks.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

type status struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Handler serves /health (the process is up) and /health/ready (the database is reachable).
type Handler struct {
	db      *pgxpool.Pool
	version string
}

func NewHandler(db *pgxpool.Pool, version string) *Handler {
	return &Handler{db: db, version: version}
}

func (h *Handler) Routes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/health", h.live)
	mux.Handle("GET "+prefix+"/health/ready", httpx.Handler(h.ready))
}

func (h *Handler) live(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, status{Status: "ok", Version: h.version})
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.Ping(ctx); err != nil {
		slog.WarnContext(ctx, "readiness check failed: database unreachable", "error", err)
		return httpx.NewError(http.StatusServiceUnavailable, "database_unavailable", "Database is not reachable.")
	}
	httpx.WriteJSON(w, http.StatusOK, status{Status: "ok", Version: h.version})
	return nil
}
