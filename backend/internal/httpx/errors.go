// Package httpx holds HTTP plumbing shared by every API module: the error
// envelope, JSON helpers, request validation and middleware.
//
// Every non-2xx response has the shape
//
//	{"error": {"code": "...", "message": "...", "request_id": "...", "details": [...]}}
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Detail describes one validation problem; Loc says where, e.g. ["body", "password"].
type Detail struct {
	Loc  []string `json:"loc"`
	Msg  string   `json:"msg"`
	Type string   `json:"type"`
}

// Error is an API error with an HTTP status and a machine-readable code.
type Error struct {
	Status  int
	Code    string
	Message string
	Details []Detail
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError returns an API error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Invalid returns a 422 validation error listing details.
func Invalid(details ...Detail) *Error {
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    "validation_error",
		Message: "The request is invalid.",
		Details: details,
	}
}

// Handler is an HTTP handler that may return an error. A returned *Error is
// written as-is; any other error becomes a logged, generic 500.
type Handler func(http.ResponseWriter, *http.Request) error

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteError(w, r, err)
	}
}

// WriteJSON writes body as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("encode response", "error", err)
	}
}

type errorBody struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id,omitempty"`
	Details   []Detail `json:"details,omitempty"`
}

// WriteError writes err using the standard error envelope.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		slog.ErrorContext(r.Context(), "unhandled error", "request_id", RequestID(r.Context()), "error", err)
		apiErr = NewError(http.StatusInternalServerError, "internal_error", "An unexpected error occurred.")
	}
	WriteJSON(w, apiErr.Status, map[string]errorBody{"error": {
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		RequestID: RequestID(r.Context()),
		Details:   apiErr.Details,
	}})
}
