// Package manifest parses local HLS and static DASH manifests into
// segment references, keeping manifest line numbers and rejecting
// path escapes and remote URIs.
package manifest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"continuity/internal/core"
)

// MaxManifestBytes bounds manifest size; MaxSegmentBytes bounds one segment.
const (
	MaxManifestBytes = 8 << 20
	MaxSegmentBytes  = 64 << 20
)

// Segment is one manifest segment entry with its origin line.
type Segment struct {
	URI      string // cleaned path relative to the manifest dir
	Line     int    // 1-based manifest line (best-effort for XML)
	Index    int64  // media sequence number
	DurTicks int64  // declared duration in 90 kHz ticks (informational)
}

// Playlist is the parsed manifest.
type Playlist struct {
	Path     string
	Dir      string
	Segments []Segment
}

// readLimited reads a local file enforcing a size cap.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, core.NewError(core.StageManifest, core.CodeInput, path, 0, -1, "open", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, core.NewError(core.StageManifest, core.CodeInput, path, 0, -1, "stat", err)
	}
	if st.Size() > limit {
		return nil, core.NewError(core.StageManifest, core.CodeOversize, path, 0, -1,
			fmt.Sprintf("size=%d limit=%d", st.Size(), limit), nil)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, core.NewError(core.StageManifest, core.CodeInput, path, 0, -1, "read", err)
	}
	return data, nil
}

// resolveURI validates a segment URI and returns its cleaned path relative
// to dir. Absolute paths, remote URIs and ".." escapes are rejected.
func resolveURI(dir, uri, source string, line int) (string, error) {
	if uri == "" {
		return "", core.NewError(core.StageManifest, core.CodeBadManifest, source, line, -1, "empty uri", nil)
	}
	if strings.Contains(uri, "://") || strings.HasPrefix(uri, "//") {
		return "", core.NewError(core.StageManifest, core.CodeBadManifest, source, line, -1,
			"remote uri not allowed: "+uri, nil)
	}
	if filepath.IsAbs(uri) {
		return "", core.NewError(core.StageManifest, core.CodePathEscape, source, line, -1,
			"absolute path: "+uri, nil)
	}
	abs := filepath.Clean(filepath.Join(dir, uri))
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", core.NewError(core.StageManifest, core.CodePathEscape, source, line, -1,
			"uri escapes manifest dir: "+uri, nil)
	}
	return rel, nil
}
