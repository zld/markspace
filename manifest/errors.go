// Package manifest parses local HLS media playlists and DASH MPDs that use
// SegmentTemplate/SegmentTimeline. All parse failures are returned as
// *Error values carrying a stable machine-readable code, asset, track,
// stage and a line number or byte offset.
package manifest

import (
	"path"
	"strconv"
	"strings"
)

// ErrorCode is a stable identifier for a manifest failure category.
type ErrorCode string

const (
	// ErrPathTraversal: URI escapes the manifest directory or is absolute.
	ErrPathTraversal ErrorCode = "MANIFEST_PATH_TRAVERSAL"
	// ErrBadDuration: zero, negative, NaN/Inf or otherwise illegal duration.
	ErrBadDuration ErrorCode = "MANIFEST_BAD_DURATION"
	// ErrZeroTimescale: explicit timescale of zero.
	ErrZeroTimescale ErrorCode = "MANIFEST_ZERO_TIMESCALE"
	// ErrOverflow: timeline arithmetic overflows int64.
	ErrOverflow ErrorCode = "MANIFEST_OVERFLOW"
	// ErrSyntax: malformed manifest text/XML.
	ErrSyntax ErrorCode = "MANIFEST_SYNTAX"
	// ErrUnsupported: feature outside the agreed local subset.
	ErrUnsupported ErrorCode = "MANIFEST_UNSUPPORTED"
)

// Error is the machine-readable failure record used across subsystems.
type Error struct {
	Code    ErrorCode `json:"code"`
	Asset   string    `json:"asset"`
	Track   string    `json:"track,omitempty"`
	Stage   string    `json:"stage"`
	Line    int       `json:"line,omitempty"`
	Offset  int64     `json:"offset,omitempty"`
	Message string    `json:"message"`
}

func (e *Error) Error() string {
	pos := ""
	if e.Line > 0 {
		pos = " line=" + strconv.Itoa(e.Line)
	} else if e.Offset > 0 {
		pos = " offset=" + strconv.FormatInt(e.Offset, 10)
	}
	track := ""
	if e.Track != "" {
		track = " track=" + e.Track
	}
	return string(e.Code) + " stage=" + e.Stage + " asset=" + e.Asset + track + pos + ": " + e.Message
}

// SafeURI rejects absolute paths, schemes and any ".." traversal segment.
// URIs are always slash-separated relative paths below the manifest dir.
func SafeURI(uri string) bool {
	if uri == "" || strings.HasPrefix(uri, "/") || strings.Contains(uri, "\\") {
		return false
	}
	if strings.Contains(uri, "://") || strings.HasPrefix(uri, "data:") {
		return false
	}
	clean := path.Clean(uri)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
