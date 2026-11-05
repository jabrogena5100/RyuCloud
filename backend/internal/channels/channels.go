// Package channels implements channel creation, lookup and owner-only updates.
package channels

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/auth"
	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

// Channel is a creator's channel. Handles are unique, lower-case and immutable.
type Channel struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"owner_id"`
	Handle      string    `json:"handle"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Summary is the channel shape embedded in video responses.
type Summary struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

var handlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,29}$`)

// reservedHandles collide with routes or would be confusing as public handles.
var reservedHandles = map[string]bool{
	"mine": true, "new": true, "admin": true, "api": true, "settings": true, "studio": true,
}

// ErrNotFound is returned for unknown channel handles or IDs.
var ErrNotFound = httpx.NewError(http.StatusNotFound, "channel_not_found", "Channel not found.")

var (
	errHandleTaken = httpx.NewError(http.StatusConflict, "handle_taken", "That channel handle is already in use.")
	errForbidden   = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this channel.")
)

// NormalizeHandle returns the stored form of a handle (handles are case-insensitive).
func NormalizeHandle(handle string) string { return strings.ToLower(strings.TrimSpace(handle)) }

// Handler serves the channel endpoints.
type Handler struct {
	db   *pgxpool.Pool
	auth *auth.Service
}

func NewHandler(db *pgxpool.Pool, authService *auth.Service) *Handler {
	return &Handler{db: db, auth: authService}
}

// Routes registers the channel endpoints. GET /channels/{handle}/videos lives in
// the videos package. The literal /channels/mine route takes precedence over
// /channels/{handle} (and "mine" is also a reserved handle).
func (h *Handler) Routes(mux *http.ServeMux, prefix string) {
	mux.Handle("POST "+prefix+"/channels", h.auth.Required(h.create))
	mux.Handle("GET "+prefix+"/channels/mine", h.auth.Required(h.listMine))
	mux.Handle("GET "+prefix+"/channels/{handle}", httpx.Handler(h.get))
	mux.Handle("PATCH "+prefix+"/channels/{handle}", h.auth.Required(h.update))
}

const columns = `id, owner_id, handle, name, description, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Channel, error) {
	var c Channel
	err := row.Scan(&c.ID, &c.OwnerID, &c.Handle, &c.Name, &c.Description, &c.CreatedAt)
	return c, err
}

func (h *Handler) byHandle(ctx context.Context, handle string) (Channel, error) {
	c, err := scan(h.db.QueryRow(ctx, `SELECT `+columns+` FROM channels WHERE handle = $1`, NormalizeHandle(handle)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return c, err
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request, user auth.User) error {
	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	handle, ok := f.TrimmedString("handle", true, 0, 100)
	handle = strings.ToLower(handle)
	switch {
	case !ok:
	case !handlePattern.MatchString(handle):
		f.AddError("handle", "pattern",
			"must be 3-30 characters of lowercase letters, digits, '_' or '-', starting with a letter or digit")
	case reservedHandles[handle]:
		f.AddError("handle", "value_error", "this handle is reserved")
	}
	name, _ := f.TrimmedString("name", true, 1, 100)
	description, _ := f.String("description", false, 0, 5000)
	if err := f.Err(); err != nil {
		return err
	}

	c, err := scan(h.db.QueryRow(r.Context(),
		`INSERT INTO channels (owner_id, handle, name, description) VALUES ($1, $2, $3, $4) RETURNING `+columns,
		user.ID, handle, name, description,
	))
	if database.IsUniqueViolation(err, "uq_channels_handle") {
		return errHandleTaken
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, c)
	return nil
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request, user auth.User) error {
	rows, err := h.db.Query(r.Context(),
		`SELECT `+columns+` FROM channels WHERE owner_id = $1 ORDER BY created_at, id`, user.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	out := []Channel{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	c, err := h.byHandle(r.Context(), r.PathValue("handle"))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, user auth.User) error {
	c, err := h.byHandle(r.Context(), r.PathValue("handle"))
	if err != nil {
		return err
	}
	if c.OwnerID != user.ID {
		return errForbidden
	}

	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	// nil means "leave unchanged"; COALESCE keeps the current value.
	var name, description *string
	if v, ok := f.TrimmedString("name", false, 1, 100); ok {
		name = &v
	}
	if v, ok := f.String("description", false, 0, 5000); ok {
		description = &v
	}
	if err := f.Err(); err != nil {
		return err
	}

	c, err = scan(h.db.QueryRow(r.Context(),
		`UPDATE channels
		 SET name = COALESCE($2, name), description = COALESCE($3, description), updated_at = now()
		 WHERE id = $1 RETURNING `+columns,
		c.ID, name, description,
	))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}
