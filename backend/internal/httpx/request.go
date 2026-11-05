package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxBodyBytes = 1 << 20 // 1 MiB; video bytes never pass through the API

// Fields reads typed values from a JSON object body and collects every
// validation problem, so a single response can report all invalid fields.
// It distinguishes missing fields from explicit nulls, which PATCH relies on.
type Fields struct {
	raw     map[string]json.RawMessage
	details []Detail
}

// DecodeObject parses the request body as a JSON object.
func DecodeObject(w http.ResponseWriter, r *http.Request) (*Fields, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, NewError(http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large.")
		}
		return nil, notAnObject()
	}
	if raw == nil { // the body was the JSON literal null
		return nil, notAnObject()
	}
	return &Fields{raw: raw}, nil
}

func notAnObject() *Error {
	return Invalid(Detail{Loc: []string{"body"}, Msg: "must be a JSON object", Type: "json_invalid"})
}

// AddError records a validation problem for a body field.
func (f *Fields) AddError(name, kind, msg string) {
	f.details = append(f.details, Detail{Loc: []string{"body", name}, Msg: msg, Type: kind})
}

// Err returns a validation error if any field was invalid, otherwise nil.
func (f *Fields) Err() error {
	if len(f.details) == 0 {
		return nil
	}
	return Invalid(f.details...)
}

// String reads a string field whose length (in characters) must be within [minLen, maxLen].
// It reports whether a valid value was present.
func (f *Fields) String(name string, required bool, minLen, maxLen int) (string, bool) {
	return f.str(name, required, minLen, maxLen, false)
}

// TrimmedString is String with surrounding whitespace removed before the length check.
func (f *Fields) TrimmedString(name string, required bool, minLen, maxLen int) (string, bool) {
	return f.str(name, required, minLen, maxLen, true)
}

func (f *Fields) str(name string, required bool, minLen, maxLen int, trim bool) (string, bool) {
	raw, present := f.raw[name]
	if !present {
		if required {
			f.AddError(name, "missing", "field required")
		}
		return "", false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		f.AddError(name, "null", "must not be null")
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		f.AddError(name, "string_type", "must be a string")
		return "", false
	}
	if trim {
		value = strings.TrimSpace(value)
	}
	switch n := utf8.RuneCountInString(value); {
	case n < minLen && minLen == 1:
		f.AddError(name, "string_too_short", "must not be empty")
		return "", false
	case n < minLen:
		f.AddError(name, "string_too_short", fmt.Sprintf("must be at least %d characters", minLen))
		return "", false
	case n > maxLen:
		f.AddError(name, "string_too_long", fmt.Sprintf("must be at most %d characters", maxLen))
		return "", false
	}
	return value, true
}

// Int64 reads an integer field that must be within [minVal, maxVal].
// It reports whether a valid value was present.
func (f *Fields) Int64(name string, required bool, minVal, maxVal int64) (int64, bool) {
	raw, present := f.raw[name]
	if !present {
		if required {
			f.AddError(name, "missing", "field required")
		}
		return 0, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		f.AddError(name, "null", "must not be null")
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		f.AddError(name, "int_type", "must be an integer")
		return 0, false
	}
	switch {
	case value < minVal:
		f.AddError(name, "int_too_small", fmt.Sprintf("must be at least %d", minVal))
		return 0, false
	case value > maxVal:
		f.AddError(name, "int_too_large", fmt.Sprintf("must be at most %d", maxVal))
		return 0, false
	}
	return value, true
}

// Email reads a required email address and returns it lower-cased.
func (f *Fields) Email(name string) string {
	value, ok := f.TrimmedString(name, true, 3, 254)
	if !ok {
		return ""
	}
	addr, err := mail.ParseAddress(value)
	at := strings.LastIndex(value, "@")
	if err != nil || addr.Address != value || at < 1 || !strings.Contains(value[at+1:], ".") {
		f.AddError(name, "value_error", "must be a valid email address")
		return ""
	}
	return strings.ToLower(value)
}

// Choice reads an optional string field that must be one of allowed.
func (f *Fields) Choice(name string, allowed ...string) (string, bool) {
	value, ok := f.String(name, false, 0, 100)
	if !ok {
		return "", false
	}
	for _, a := range allowed {
		if value == a {
			return value, true
		}
	}
	f.AddError(name, "enum", "must be one of: "+strings.Join(allowed, ", "))
	return "", false
}

// UUID reads a required UUID field and returns it in canonical lower case.
func (f *Fields) UUID(name string) string {
	value, ok := f.String(name, true, 0, 100)
	if !ok {
		return ""
	}
	if !IsUUID(value) {
		f.AddError(name, "uuid_parsing", "must be a UUID")
		return ""
	}
	return strings.ToLower(value)
}

// QueryInt parses an optional integer query parameter that must be within [minVal, maxVal].
func QueryInt(r *http.Request, name string, fallback, minVal, maxVal int) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < minVal || n > maxVal {
		return 0, Invalid(Detail{
			Loc:  []string{"query", name},
			Msg:  fmt.Sprintf("must be an integer from %d to %d", minVal, maxVal),
			Type: "value_error",
		})
	}
	return n, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a hyphenated UUID string.
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// PathUUID returns the named path wildcard, which must be a UUID.
func PathUUID(r *http.Request, name string) (string, error) {
	value := r.PathValue(name)
	if !IsUUID(value) {
		return "", Invalid(Detail{Loc: []string{"path", name}, Msg: "must be a UUID", Type: "uuid_parsing"})
	}
	return strings.ToLower(value), nil
}
