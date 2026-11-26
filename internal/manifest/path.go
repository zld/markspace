// Package manifest parses the local static DASH (MPD) and HLS (m3u8) sources
// medtrace accepts. Parsing is strict: every error names the input file and,
// where the dialect allows it, the 1-based line that caused it, and no index
// is written when a manifest is rejected.
package manifest

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"medtrace/internal/core"
)

// ref is a manifest-referenced media path plus the line that referenced it.
type ref struct {
	// URIPath is the manifest-relative path as written, after URL decoding.
	URIPath string
	Line    int
}

// resolveRel joins a manifest-relative reference against the manifest's own
// directory and enforces that the result stays inside the media root. The
// returned path is root-relative and slash-separated so reports are portable
// across the macOS and Linux checkouts this project is built on.
func resolveRel(manifestRel string, r ref) (string, *core.Fault) {
	pos := core.Pos{File: manifestRel, Line: r.Line, Byte: -1}
	if strings.TrimSpace(r.URIPath) == "" {
		return "", core.NewFault(core.KindManifest, pos, "resolve", "empty segment reference")
	}
	raw := r.URIPath
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		return "", core.NewFault(core.KindPath, pos, "resolve",
			fmt.Sprintf("remote reference %q is not supported; only local files are accepted", raw))
	}
	if err := checkRelative(raw); err != nil {
		return "", core.NewFault(core.KindPath, pos, "resolve", err.Error())
	}
	joined := path.Join(path.Dir(filepath.ToSlash(manifestRel)), raw)
	clean := path.Clean(joined)
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", core.NewFault(core.KindPath, pos, "resolve",
			fmt.Sprintf("reference %q escapes the media root", raw))
	}
	return clean, nil
}

// checkRelative rejects absolute paths and Windows drive/UNC forms so a
// manifest cannot name a file outside the workspace on any platform.
func checkRelative(raw string) error {
	if path.IsAbs(raw) || filepath.IsAbs(raw) {
		return fmt.Errorf("absolute reference %q is not allowed", raw)
	}
	if strings.HasPrefix(raw, "\\") || (len(raw) >= 2 && raw[1] == ':') {
		return fmt.Errorf("absolute reference %q is not allowed", raw)
	}
	return nil
}

// Abs joins a root-relative, already-validated media path onto the absolute
// workspace root and re-checks containment via core.SafeJoin.
func Abs(root, rel string) (string, error) { return core.SafeJoin(root, rel) }

// ResolveRooted validates a root-relative reference written without a
// manifest context (for example a segment named by a client event) and returns
// its cleaned root-relative form.
func ResolveRooted(manifestRel, raw string) (string, *core.Fault) {
	return resolveRel(manifestRel, ref{URIPath: raw, Line: 0})
}
