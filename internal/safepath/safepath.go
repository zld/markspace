// Package safepath enforces output-path safety: every artifact name is
// constrained below its work root (no ".." traversal / absolute escape) and
// output locations must be local filesystem paths (no http(s)://, s3://,
// scp-like or UNC/Windows-remote targets).
package safepath

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	merr "mediapack/internal/errors"
)

var (
	scpLike  = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:`)
	winDrive = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// ValidateLocalOutput checks a user-supplied output location is local.
// A file:// URL is accepted and converted to a filesystem path.
func ValidateLocalOutput(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", merr.New(merr.CatPathRejected, 0, "empty output path")
	}
	if strings.Contains(p, "://") {
		u, err := url.Parse(p)
		if err != nil {
			return "", merr.New(merr.CatPathRejected, 0, "unparseable location %q: %v", p, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "file":
			if u.Host != "" && u.Host != "localhost" {
				return "", merr.New(merr.CatPathRejected, 0, "non-local file host %q", u.Host)
			}
			return filepath.Clean(u.Path), nil
		default:
			return "", merr.New(merr.CatPathRejected, 0, "non-local output scheme %q in %q", u.Scheme, p)
		}
	}
	if strings.HasPrefix(p, `\\`) {
		return "", merr.New(merr.CatPathRejected, 0, "UNC network path rejected: %q", p)
	}
	// scp-like user@host:path and Windows drive prefixes are not local macOS
	// output locations.
	if winDrive.MatchString(p) {
		return "", merr.New(merr.CatPathRejected, 0, "windows-drive path rejected on darwin: %q", p)
	}
	if m := scpLike.FindStringSubmatch(p); m != nil {
		return "", merr.New(merr.CatPathRejected, 0, "remote/non-local location rejected: %q", p)
	}
	return filepath.Clean(p), nil
}

// JoinUnder joins name elements under root and guarantees the result stays
// within root. Elements must be relative and clean of traversal.
func JoinUnder(root string, elems ...string) (string, error) {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", merr.New(merr.CatPathRejected, 0, "bad root %q: %v", root, err)
	}
	cleanElems := make([]string, 0, len(elems)+1)
	cleanElems = append(cleanElems, absRoot)
	for _, e := range elems {
		if e == "" || e == "." {
			continue
		}
		if strings.ContainsRune(e, 0) {
			return "", merr.New(merr.CatPathRejected, 0, "NUL byte in path element")
		}
		if filepath.IsAbs(e) || strings.HasPrefix(e, `\`) {
			return "", merr.New(merr.CatPathRejected, 0, "absolute path element rejected: %q", e)
		}
		c := filepath.Clean(e)
		if c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
			return "", merr.New(merr.CatPathRejected, 0, "path traversal rejected: %q", e)
		}
		cleanElems = append(cleanElems, c)
	}
	joined := filepath.Join(cleanElems...)
	rel, err := filepath.Rel(absRoot, joined)
	if err != nil {
		return "", merr.New(merr.CatPathRejected, 0, "cannot relativize %q: %v", joined, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", merr.New(merr.CatPathRejected, 0, "path escapes work root: %q", joined)
	}
	return joined, nil
}

// MustRel is JoinUnder but returns a relative slash path instead of absolute.
func MustRel(elems ...string) (string, error) {
	for _, e := range elems {
		if filepath.IsAbs(e) {
			return "", fmt.Errorf("absolute element %q", e)
		}
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(elems...)))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("traversal %q", c)
	}
	return c, nil
}
