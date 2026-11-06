// Package videos implements video metadata, visibility rules and the home feed.
//
// Lifecycle (the PENDING_UPLOAD/UPLOADING/QUEUED/PROCESSING/READY/FAILED states, kept
// lower-case in the API as draft/uploading/queued/processing/ready/failed):
//
//	draft -> uploading -> queued -> processing -> ready
//	                         ^           |  \-> failed
//	                         +-- retry --+
//
// Creating metadata makes a draft; the uploads package moves a video to uploading and,
// once the file is verified, to queued (creating its processing job). The worker moves
// it to processing and finally ready (or failed). Only ready videos are playable or
// publicly listable.
package videos

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/auth"
	"github.com/jabrogena5100/RyuCloud/backend/internal/channels"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
)

// Status and visibility values; the database enforces them with CHECK constraints.
const (
	StatusDraft      = "draft"
	StatusUploading  = "uploading"
	StatusQueued     = "queued"
	StatusProcessing = "processing"
	StatusReady      = "ready"
	StatusFailed     = "failed"

	VisibilityPublic   = "public"
	VisibilityUnlisted = "unlisted"
	VisibilityPrivate  = "private"
)

var visibilities = []string{VisibilityPublic, VisibilityUnlisted, VisibilityPrivate}

// Video is the API representation of a video.
type Video struct {
	ID              string           `json:"id"`
	Channel         channels.Summary `json:"channel"`
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	Status          string           `json:"status"`
	Visibility      string           `json:"visibility"`
	DurationSeconds *int32           `json:"duration_seconds"`
	ViewCount       int64            `json:"view_count"`
	CreatedAt       time.Time        `json:"created_at"`
	PublishedAt     *time.Time       `json:"published_at"`
	// Resolved by the PlaybackURLProvider; null until the video has been processed.
	ThumbnailURL *string `json:"thumbnail_url"`
	PlaybackURL  *string `json:"playback_url"`
	// Processing progress (0-100) while queued or processing; null otherwise.
	ProcessingProgress *int `json:"processing_progress"`
	// Why processing failed, in words meant for the owner; null unless failed.
	ProcessingError *string `json:"processing_error"`

	ownerID      string
	thumbnailKey *string
	manifestKey  *string
}

// Page is a paginated list of videos.
type Page struct {
	Items  []Video `json:"items"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

var (
	errNotFound         = httpx.NewError(http.StatusNotFound, "video_not_found", "Video not found.")
	errForbidden        = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this video.")
	errChannelForbidden = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this channel.")
)

// Handler serves the video endpoints.
type Handler struct {
	db       *pgxpool.Pool
	auth     *auth.Service
	playback integrations.PlaybackURLProvider
	storage  integrations.ObjectStorage
}

func NewHandler(db *pgxpool.Pool, authService *auth.Service, playback integrations.PlaybackURLProvider, storage integrations.ObjectStorage) *Handler {
	return &Handler{db: db, auth: authService, playback: playback, storage: storage}
}

// Routes registers the video endpoints, including GET /channels/{handle}/videos.
func (h *Handler) Routes(mux *http.ServeMux, prefix string) {
	mux.Handle("GET "+prefix+"/videos", httpx.Handler(h.feed))
	mux.Handle("POST "+prefix+"/videos", h.auth.Required(h.create))
	mux.Handle("GET "+prefix+"/videos/{id}", h.auth.Optional(h.get))
	mux.Handle("PATCH "+prefix+"/videos/{id}", h.auth.Required(h.update))
	mux.Handle("DELETE "+prefix+"/videos/{id}", h.auth.Required(h.remove))
	mux.Handle("GET "+prefix+"/videos/{id}/hls/{path...}", h.auth.Optional(h.hls))
	mux.Handle("GET "+prefix+"/videos/{id}/thumbnail", h.auth.Optional(h.thumbnail))
	mux.Handle("GET "+prefix+"/channels/{handle}/videos", h.auth.Optional(h.channelVideos))
}

const (
	selectVideos = `SELECT v.id, v.title, v.description, v.status, v.visibility, v.duration_seconds,
		v.view_count, v.created_at, v.published_at, v.thumbnail_object_key, v.hls_manifest_key,
		c.id, c.handle, c.name, c.owner_id, j.progress, v.processing_error
	FROM videos v JOIN channels c ON c.id = v.channel_id
	LEFT JOIN processing_jobs j ON j.video_id = v.id`
	publiclyListed = `v.visibility = 'public' AND v.status = 'ready'`
	newestFirst    = `ORDER BY v.published_at DESC NULLS LAST, v.created_at DESC, v.id`
)

type scanner interface {
	Scan(dest ...any) error
}

func scanVideo(row scanner) (Video, error) {
	var (
		v        Video
		progress *int16
	)
	err := row.Scan(&v.ID, &v.Title, &v.Description, &v.Status, &v.Visibility, &v.DurationSeconds,
		&v.ViewCount, &v.CreatedAt, &v.PublishedAt, &v.thumbnailKey, &v.manifestKey,
		&v.Channel.ID, &v.Channel.Handle, &v.Channel.Name, &v.ownerID, &progress, &v.ProcessingError)
	if progress != nil && (v.Status == StatusQueued || v.Status == StatusProcessing) {
		p := int(*progress)
		v.ProcessingProgress = &p
	}
	return v, err
}

func (h *Handler) withURLs(v Video) Video {
	v.ThumbnailURL = h.playback.ThumbnailURL(v.ID, v.thumbnailKey)
	v.PlaybackURL = h.playback.PlaybackURL(v.ID, v.manifestKey)
	return v
}

func (h *Handler) list(ctx context.Context, query string, args ...any) ([]Video, error) {
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h.withURLs(v))
	}
	return out, rows.Err()
}

func (h *Handler) byID(ctx context.Context, id string) (Video, error) {
	v, err := scanVideo(h.db.QueryRow(ctx, selectVideos+` WHERE v.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Video{}, errNotFound
	}
	if err != nil {
		return Video{}, err
	}
	return h.withURLs(v), nil
}

func isOwner(v Video, user *auth.User) bool {
	return user != nil && v.ownerID == user.ID
}

// canView: owners see everything; others only ready videos that are public
// (listed in feeds) or unlisted (reachable by ID).
func canView(v Video, user *auth.User) bool {
	if isOwner(v, user) {
		return true
	}
	return v.Status == StatusReady && (v.Visibility == VisibilityPublic || v.Visibility == VisibilityUnlisted)
}

// viewable loads the {id} video. Videos the caller may not see are reported as
// missing, so private IDs don't leak.
func (h *Handler) viewable(r *http.Request, user *auth.User) (Video, error) {
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return Video{}, err
	}
	v, err := h.byID(r.Context(), id)
	if err != nil {
		return Video{}, err
	}
	if !canView(v, user) {
		return Video{}, errNotFound
	}
	return v, nil
}

func (h *Handler) owned(r *http.Request, user auth.User) (Video, error) {
	v, err := h.viewable(r, &user)
	if err != nil {
		return Video{}, err
	}
	if !isOwner(v, &user) {
		return Video{}, errForbidden
	}
	return v, nil
}

func pagination(r *http.Request) (limit, offset int, err error) {
	if limit, err = httpx.QueryInt(r, "limit", 24, 1, 50); err != nil {
		return 0, 0, err
	}
	if offset, err = httpx.QueryInt(r, "offset", 0, 0, 10_000); err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

// feed is the home feed: public, ready videos, newest first.
func (h *Handler) feed(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	videos, err := h.list(r.Context(),
		selectVideos+` WHERE `+publiclyListed+` `+newestFirst+` LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, Page{Items: videos, Limit: limit, Offset: offset})
	return nil
}

// channelVideos lists a channel's public, ready videos; the owner sees all of them.
func (h *Handler) channelVideos(w http.ResponseWriter, r *http.Request, user *auth.User) error {
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	var channelID, ownerID string
	err = h.db.QueryRow(r.Context(), `SELECT id, owner_id FROM channels WHERE handle = $1`,
		channels.NormalizeHandle(r.PathValue("handle"))).Scan(&channelID, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return channels.ErrNotFound
	}
	if err != nil {
		return err
	}

	query := selectVideos + ` WHERE v.channel_id = $1`
	if user == nil || user.ID != ownerID {
		query += ` AND ` + publiclyListed
	}
	videos, err := h.list(r.Context(), query+` `+newestFirst+` LIMIT $2 OFFSET $3`, channelID, limit, offset)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, Page{Items: videos, Limit: limit, Offset: offset})
	return nil
}

// create stores metadata only; uploading media arrives in a later phase.
func (h *Handler) create(w http.ResponseWriter, r *http.Request, user auth.User) error {
	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	channelID := f.UUID("channel_id")
	title, _ := f.TrimmedString("title", true, 1, 100)
	description, _ := f.String("description", false, 0, 5000)
	visibility, ok := f.Choice("visibility", visibilities...)
	if !ok {
		visibility = VisibilityPrivate
	}
	if err := f.Err(); err != nil {
		return err
	}

	ctx := r.Context()
	var ownerID string
	err = h.db.QueryRow(ctx, `SELECT owner_id FROM channels WHERE id = $1`, channelID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return channels.ErrNotFound
	}
	if err != nil {
		return err
	}
	if ownerID != user.ID {
		return errChannelForbidden
	}

	var id string
	if err := h.db.QueryRow(ctx,
		`INSERT INTO videos (channel_id, title, description, visibility) VALUES ($1, $2, $3, $4) RETURNING id`,
		channelID, title, description, visibility,
	).Scan(&id); err != nil {
		return err
	}
	v, err := h.byID(ctx, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, v)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, user *auth.User) error {
	v, err := h.viewable(r, user)
	if err != nil {
		return err
	}
	// With CloudFront delivery the playback URL only works for a viewer holding signed
	// cookies. They are issued here, right after the visibility check above, and are
	// scoped to this one video.
	if g, ok := h.playback.(integrations.PlaybackGrantor); ok && v.PlaybackURL != nil && v.Status == StatusReady {
		g.GrantPlayback(w, v.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, v)
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, user auth.User) error {
	v, err := h.owned(r, user)
	if err != nil {
		return err
	}
	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	// nil means "leave unchanged"; COALESCE keeps the current value.
	var title, description, visibility *string
	if s, ok := f.TrimmedString("title", false, 1, 100); ok {
		title = &s
	}
	if s, ok := f.String("description", false, 0, 5000); ok {
		description = &s
	}
	if s, ok := f.Choice("visibility", visibilities...); ok {
		visibility = &s
	}
	if err := f.Err(); err != nil {
		return err
	}

	ctx := r.Context()
	if _, err := h.db.Exec(ctx,
		`UPDATE videos
		 SET title = COALESCE($2, title), description = COALESCE($3, description),
		     visibility = COALESCE($4, visibility), updated_at = now()
		 WHERE id = $1`,
		v.ID, title, description, visibility,
	); err != nil {
		return err
	}
	updated, err := h.byID(ctx, v.ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, updated)
	return nil
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request, user auth.User) error {
	v, err := h.owned(r, user)
	if err != nil {
		return err
	}
	ctx := r.Context()

	// Collect the storage keys before the rows (and their upload records) cascade away.
	rows, err := h.db.Query(ctx, `SELECT object_key FROM video_uploads WHERE video_id = $1`, v.ID)
	if err != nil {
		return err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}

	if _, err := h.db.Exec(ctx, `DELETE FROM videos WHERE id = $1`, v.ID); err != nil {
		return err
	}

	// Best effort: the video is already gone, so a storage hiccup must not fail the
	// request. Leftover objects are cleaned up by a bucket lifecycle rule (docs/uploads.md).
	for _, key := range keys {
		if err := h.storage.DeleteObject(ctx, key); err != nil && !errors.Is(err, integrations.ErrNotConfigured) {
			slog.WarnContext(ctx, "could not delete video object", "request_id", httpx.RequestID(ctx), "object_key", key, "error", err)
		}
	}
	// Processed output (HLS renditions and thumbnail) lives under one prefix.
	if _, err := h.storage.DeletePrefix(ctx, integrations.VideoPrefix(v.ID)); err != nil && !errors.Is(err, integrations.ErrNotConfigured) {
		slog.WarnContext(ctx, "could not delete processed video output", "request_id", httpx.RequestID(ctx), "video_id", v.ID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
