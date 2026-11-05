// Package server wires every API module into one HTTP handler.
package server

import (
	_ "embed"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/auth"
	"github.com/jabrogena5100/RyuCloud/backend/internal/channels"
	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
	"github.com/jabrogena5100/RyuCloud/backend/internal/health"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/uploads"
	"github.com/jabrogena5100/RyuCloud/backend/internal/videos"
)

const (
	// APIPrefix is the versioned base path of every route.
	APIPrefix = "/api/v1"
	Version   = "0.2.0"
)

//go:embed openapi.yaml
var openAPISpec []byte

// Option customises the handler built by New.
type Option func(*options)

type options struct {
	storage integrations.ObjectStorage
}

// WithStorage sets the object store used for uploads. The default fails every
// storage call with "not configured", so uploads are off unless storage is wired in.
func WithStorage(storage integrations.ObjectStorage) Option {
	return func(o *options) { o.storage = storage }
}

// New returns the API handler: versioned routes behind the shared middleware.
func New(cfg config.Config, db *pgxpool.Pool, logger *slog.Logger, opts ...Option) http.Handler {
	o := options{storage: integrations.DisabledStorage{}}
	for _, opt := range opts {
		opt(&o)
	}

	mux := http.NewServeMux()
	authService := auth.NewService(db, cfg.SessionCookieName, cfg.SessionTTL, cfg.CookieSecure)

	health.NewHandler(db, Version).Routes(mux, APIPrefix)
	authService.Routes(mux, APIPrefix)
	channels.NewHandler(db, authService).Routes(mux, APIPrefix)
	videos.NewHandler(db, authService, integrations.NewPlaybackProvider(), o.storage).Routes(mux, APIPrefix)

	uploadOpts := uploads.Options{MaxBytes: cfg.Upload.MaxBytes, URLTTL: cfg.Upload.URLTTL}
	uploads.NewHandler(uploads.NewService(db, o.storage, uploadOpts), authService, uploadOpts).Routes(mux, APIPrefix)

	if cfg.EnableAPIDocs {
		mux.HandleFunc("GET "+APIPrefix+"/openapi.yaml", serveSpec)
		mux.HandleFunc("GET "+APIPrefix+"/docs", serveDocs)
	}
	return httpx.Middleware(logger, cfg.TrustedOrigins, httpx.JSONFallback(mux))
}

func serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}

// docsPage renders Swagger UI (pinned version, loaded from a CDN) for the embedded spec.
const docsPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>RyuCloud API docs</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>window.ui = SwaggerUIBundle({ url: "openapi.yaml", dom_id: "#swagger-ui" });</script>
</body>
</html>
`

func serveDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}
