package videos

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"github.com/jabrogena5100/RyuCloud/backend/internal/auth"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
)

// Local-development media delivery. Storage stays private; the API checks the same
// visibility rules as GET /videos/{id} and streams the object. This is deliberately
// the *only* place that reads media, so Phase 4 can replace it with CDN URLs
// (the PlaybackURLProvider already abstracts the URLs the clients see).

var errMediaNotFound = httpx.NewError(http.StatusNotFound, "media_not_found", "Media not found.")

// hlsPath whitelists exactly the files the worker produces. It rejects anything with
// "..", extra slashes or other names before a storage key is ever built.
var hlsPath = regexp.MustCompile(`^(master\.m3u8|[0-9]{2,4}p/(index\.m3u8|seg_[0-9]{5}\.ts))$`)

func hlsContentType(path string) string {
	if len(path) > 5 && path[len(path)-5:] == ".m3u8" {
		return "application/vnd.apple.mpegurl"
	}
	return "video/mp2t"
}

// hls serves a playlist or segment of a ready video.
func (h *Handler) hls(w http.ResponseWriter, r *http.Request, user *auth.User) error {
	v, err := h.viewable(r, user)
	if err != nil {
		return err
	}
	rel := r.PathValue("path")
	if v.Status != StatusReady || v.manifestKey == nil || !hlsPath.MatchString(rel) {
		return errMediaNotFound
	}
	return h.serveObject(w, r, integrations.HLSKey(v.ID, rel), hlsContentType(rel))
}

// thumbnail serves a video's poster image.
func (h *Handler) thumbnail(w http.ResponseWriter, r *http.Request, user *auth.User) error {
	v, err := h.viewable(r, user)
	if err != nil {
		return err
	}
	if v.thumbnailKey == nil {
		return errMediaNotFound
	}
	return h.serveObject(w, r, *v.thumbnailKey, "image/jpeg")
}

func (h *Handler) serveObject(w http.ResponseWriter, r *http.Request, key, contentType string) error {
	obj, err := h.storage.GetObject(r.Context(), key)
	switch {
	case errors.Is(err, integrations.ErrObjectNotFound):
		return errMediaNotFound
	case errors.Is(err, integrations.ErrNotConfigured):
		return httpx.NewError(http.StatusServiceUnavailable, "storage_not_configured", "Object storage is not configured.")
	case err != nil:
		logStorageError(r.Context(), key, err)
		return httpx.NewError(http.StatusBadGateway, "storage_unavailable", "Object storage is unavailable. Please try again.")
	}
	defer obj.Body.Close()

	hdr := w.Header()
	hdr.Set("Content-Type", contentType) // never the stored type: the API decides what browsers see
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "private, max-age=300")
	if obj.Size > 0 {
		hdr.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	if _, err := io.Copy(w, obj.Body); err != nil {
		// Headers are already sent; the client sees a truncated body. Just record it.
		logStorageError(r.Context(), key, err)
	}
	return nil
}

func logStorageError(ctx context.Context, key string, err error) {
	slog.ErrorContext(ctx, "media read failed", "request_id", httpx.RequestID(ctx), "object_key", key, "error", err)
}
