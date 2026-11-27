// Package fsx provides sandboxed local-path access. Every input or output
// path must resolve inside an explicitly supplied base directory; symlinks
// pointing outside the base are rejected so fixtures cannot escape the
// configured workspace.
package fsx

import (
	"os"
	"path/filepath"
	"strings"

	"mediakernel/internal/clock"
)

// Resolve cleans and bounds path to base. Both arguments must be absolute.
// It evaluates symlinks on existing components and rejects any escape from
// the base tree, including a symlink whose target leaves the sandbox.
func Resolve(base, path string) (string, error) {
	absBase, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", clock.NewError("fsx", clock.CatInvalidParam,
			"invalid base %q", base).Wrap(err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(absBase, path)
	}
	clean := filepath.Clean(path)
	if !inside(absBase, clean) {
		return "", clock.NewError("fsx", clock.CatSymlinkEscape,
			"path %q escapes base %q", clean, absBase)
	}

	// Walk the components that exist on disk, following symlinks, and
	// re-check containment after every resolution step.
	cur := absBase
	rel, err := filepath.Rel(absBase, clean)
	if err != nil {
		return "", clock.NewError("fsx", clock.CatSymlinkEscape,
			"cannot relativize %q", clean).Wrap(err)
	}
	if rel == "." {
		return absBase, nil
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				// Remaining leaf components do not exist yet (output path):
				// lexical containment above is the full check for them.
				cur = next
				continue
			}
			return "", clock.NewError("fsx", clock.CatInvalidParam,
				"lstat %q failed", next).Wrap(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(next)
			if err != nil {
				return "", clock.NewError("fsx", clock.CatSymlinkEscape,
					"unreadable symlink %q", next).Wrap(err)
			}
			resolved := target
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(next), resolved)
			}
			resolved = filepath.Clean(resolved)
			if !inside(absBase, resolved) {
				return "", clock.NewError("fsx", clock.CatSymlinkEscape,
					"symlink %q -> %q escapes base %q", next, resolved, absBase)
			}
			cur = resolved
			continue
		}
		cur = next
	}
	if !inside(absBase, clean) {
		return "", clock.NewError("fsx", clock.CatSymlinkEscape,
			"resolved path %q escapes base %q", cur, absBase)
	}
	return clean, nil
}

func inside(base, path string) bool {
	if path == base {
		return true
	}
	return strings.HasPrefix(path, base+string(os.PathSeparator))
}

// OpenInput resolves path inside base and opens it read-only. Symlinks are
// re-checked through Resolve before opening.
func OpenInput(base, path string) (*os.File, string, error) {
	clean, err := Resolve(base, path)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, "", clock.NewError("fsx", clock.CatNotFound,
			"open %q failed", clean).Wrap(err)
	}
	return f, clean, nil
}

// EnsureOutput resolves an output path and creates its parent directory
// inside base, never following a symlink that escapes the sandbox.
func EnsureOutput(base, path string) (string, error) {
	clean, err := Resolve(base, path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(clean)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", clock.NewError("fsx", clock.CatInvalidParam,
			"mkdir %q failed", dir).Wrap(err)
	}
	return clean, nil
}
