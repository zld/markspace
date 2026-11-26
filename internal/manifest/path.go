package manifest

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"liveregress/internal/fault"
)

// SecureJoin resolves a playlist URI against an input root and rejects any
// path that escapes it. It returns the absolute path, the cleaned
// slash-separated path relative to the root, and an error.
//
// Rejection covers: absolute paths, backslashes, NUL bytes, URL schemes,
// query strings, percent-encoded traversal, lexical ".." escapes and
// symlinks that resolve outside the root.
func SecureJoin(root, rel string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", fault.New(fault.KindPath, rel, -1, "empty input root")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", "", fault.Wrap(fault.KindPath, rel, -1, "input root", err)
	}
	realBase := base
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		realBase = resolved
	}
	if info, err := os.Stat(realBase); err != nil || !info.IsDir() {
		return "", "", fault.New(fault.KindPath, rel, -1, "input root is not a readable directory")
	}

	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", "", fault.New(fault.KindPath, rel, -1, "empty relative path")
	}
	if strings.ContainsRune(rel, 0) {
		return "", "", fault.New(fault.KindPath, rel, -1, "NUL byte in path")
	}
	if strings.Contains(rel, "\\") {
		return "", "", fault.New(fault.KindPath, rel, 0, "backslash in path")
	}
	if strings.Contains(rel, "://") {
		return "", "", fault.New(fault.KindPath, rel, 0, "absolute URL is not allowed")
	}
	u, err := url.Parse(rel)
	if err != nil {
		return "", "", fault.Wrap(fault.KindPath, rel, 0, "malformed URI", err)
	}
	if u.Scheme != "" || u.Host != "" {
		return "", "", fault.New(fault.KindPath, rel, 0, "only relative local URIs are allowed")
	}
	if u.RawQuery != "" {
		return "", "", fault.New(fault.KindPath, rel, 0, "query strings are not allowed")
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", "", fault.Wrap(fault.KindPath, rel, 0, "malformed percent escape", err)
	}
	if decoded == "" || strings.HasPrefix(decoded, "/") || filepath.IsAbs(decoded) {
		return "", "", fault.New(fault.KindPath, rel, 0, "absolute paths are not allowed")
	}
	clean := path.Clean(decoded)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", fault.New(fault.KindPath, rel, 0, "path escapes input root")
	}
	joined := filepath.Join(realBase, filepath.FromSlash(clean))
	back, err := filepath.Rel(realBase, joined)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", "", fault.New(fault.KindPath, clean, 0, "path escapes input root")
	}
	if real, err := filepath.EvalSymlinks(joined); err == nil {
		contained, err := filepath.Rel(realBase, real)
		if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
			return "", "", fault.New(fault.KindPath, clean, 0, "symlink escapes input root")
		}
	}
	return joined, clean, nil
}

// ResolveRelative joins a URI relative to the playlist that referenced it.
// dir is the slash-separated directory of the referring playlist, "" for root.
func ResolveRelative(dir, uri string) string {
	if dir == "" || dir == "." {
		return uri
	}
	if strings.HasPrefix(uri, "/") || strings.Contains(uri, "://") {
		return uri // SecureJoin rejects it with a precise fault
	}
	return path.Join(dir, uri)
}

// JoinRel is path.Join for slash-separated relative paths.
func JoinRel(parts ...string) string { return path.Join(parts...) }

// Dir returns the parent directory of a slash-separated relative path.
func Dir(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

func pathError(rel string, off int64, format string, args ...any) error {
	return fault.New(fault.KindPath, rel, off, fmt.Sprintf(format, args...))
}
