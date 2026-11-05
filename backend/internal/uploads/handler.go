package uploads

import (
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/jabrogena5100/RyuCloud/backend/internal/auth"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

var visibilities = []string{"public", "unlisted", "private"}

var uploadStatuses = []string{StatusPending, StatusUploaded, StatusFailed, StatusCancelled, StatusExpired}

// Handler serves the upload endpoints.
type Handler struct {
	svc  *Service
	auth *auth.Service
	opts Options
}

func NewHandler(svc *Service, authService *auth.Service, opts Options) *Handler {
	return &Handler{svc: svc, auth: authService, opts: opts}
}

// Routes registers the upload endpoints. The literal /uploads/limits route takes
// precedence over /uploads/{id}.
func (h *Handler) Routes(mux *http.ServeMux, prefix string) {
	mux.Handle("GET "+prefix+"/uploads/limits", httpx.Handler(h.limits))
	mux.Handle("POST "+prefix+"/uploads", h.auth.Required(h.initiate))
	mux.Handle("GET "+prefix+"/uploads", h.auth.Required(h.list))
	mux.Handle("GET "+prefix+"/uploads/{id}", h.auth.Required(h.get))
	mux.Handle("POST "+prefix+"/uploads/{id}/complete", h.auth.Required(h.complete))
	mux.Handle("DELETE "+prefix+"/uploads/{id}", h.auth.Required(h.cancel))
}

// limits tells clients what they may upload, so the UI can validate before sending anything.
func (h *Handler) limits(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"max_bytes":             h.opts.MaxBytes,
		"allowed_content_types": AllowedContentTypes(),
		"allowed_extensions":    AllowedExtensions(),
		"url_ttl_seconds":       int(h.opts.URLTTL.Seconds()),
	})
	return nil
}

func (h *Handler) initiate(w http.ResponseWriter, r *http.Request, user auth.User) error {
	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	channelID := f.UUID("channel_id")
	title, _ := f.TrimmedString("title", true, 1, 100)
	description, _ := f.String("description", false, 0, 5000)
	visibility, ok := f.Choice("visibility", visibilities...)
	if !ok {
		visibility = "private"
	}
	filename, hasFilename := f.TrimmedString("filename", true, 1, 255)
	if hasFilename {
		if problem := filenameProblem(filename); problem != "" {
			f.AddError("filename", "value_error", problem)
		}
	}
	rawType, _ := f.TrimmedString("content_type", true, 1, 100)
	// The upper bound is enforced below with a dedicated 413, not a generic 422.
	size, _ := f.Int64("size_bytes", true, 1, math.MaxInt64)
	if err := f.Err(); err != nil {
		return err
	}

	contentType := normalizeContentType(rawType)
	if !contentTypeAllowed(contentType) {
		return httpx.NewError(http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Unsupported file type. Allowed types: "+strings.Join(AllowedContentTypes(), ", ")+".")
	}
	if !extensionMatches(filename, contentType) {
		return httpx.NewError(http.StatusUnsupportedMediaType, "unsupported_media_type",
			"The file extension does not match its type. Allowed extensions: "+strings.Join(AllowedExtensions(), ", ")+".")
	}
	if size > h.opts.MaxBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("The file is too large. The maximum size is %d bytes.", h.opts.MaxBytes))
	}

	result, err := h.svc.Initiate(r.Context(), user.ID, InitiateParams{
		ChannelID:   channelID,
		Title:       title,
		Description: description,
		Visibility:  visibility,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   size,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, user auth.User) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	up, err := h.svc.Get(r.Context(), id, user.ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, up)
	return nil
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request, user auth.User) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	up, err := h.svc.Complete(r.Context(), id, user.ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, up)
	return nil
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request, user auth.User) error {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := h.svc.Cancel(r.Context(), id, user.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, user auth.User) error {
	limit, err := httpx.QueryInt(r, "limit", 50, 1, 100)
	if err != nil {
		return err
	}
	offset, err := httpx.QueryInt(r, "offset", 0, 0, 10_000)
	if err != nil {
		return err
	}
	status := r.URL.Query().Get("status")
	if status != "" {
		valid := false
		for _, s := range uploadStatuses {
			valid = valid || s == status
		}
		if !valid {
			return httpx.Invalid(httpx.Detail{
				Loc:  []string{"query", "status"},
				Msg:  "must be one of: " + strings.Join(uploadStatuses, ", "),
				Type: "enum",
			})
		}
	}

	items, err := h.svc.List(r.Context(), user.ID, status, limit, offset)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}
