package uploads

import (
	"path"
	"sort"
	"strings"
	"unicode"
)

// allowedTypes maps each accepted media type to the file extensions that may
// carry it. Both must agree, which stops, for example, an .exe declared as video/mp4.
var allowedTypes = map[string][]string{
	"video/mp4":        {".mp4", ".m4v"},
	"video/quicktime":  {".mov"},
	"video/webm":       {".webm"},
	"video/x-matroska": {".mkv"},
}

// AllowedContentTypes returns the accepted media types, sorted.
func AllowedContentTypes() []string {
	types := make([]string, 0, len(allowedTypes))
	for t := range allowedTypes {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// AllowedExtensions returns the accepted file extensions, sorted.
func AllowedExtensions() []string {
	var exts []string
	for _, list := range allowedTypes {
		exts = append(exts, list...)
	}
	sort.Strings(exts)
	return exts
}

// normalizeContentType lower-cases a media type and drops parameters ("; codecs=...").
func normalizeContentType(value string) string {
	base, _, _ := strings.Cut(value, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

// contentTypeAllowed reports whether the (normalized) type is accepted.
func contentTypeAllowed(contentType string) bool {
	_, ok := allowedTypes[contentType]
	return ok
}

// extensionMatches reports whether filename ends in an extension valid for contentType.
func extensionMatches(filename, contentType string) bool {
	ext := strings.ToLower(path.Ext(filename))
	for _, allowed := range allowedTypes[contentType] {
		if ext == allowed {
			return true
		}
	}
	return false
}

// filenameProblem describes why a client-supplied filename is unacceptable, or
// returns "" when it is fine. The name is stored for display only; it never
// becomes part of an object key.
func filenameProblem(filename string) string {
	if strings.ContainsAny(filename, `/\`) {
		return "must not contain path separators"
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return "must not contain control characters"
		}
	}
	return ""
}
