package media

import (
	"os"
	"path/filepath"
	"strings"
)

// SafeOpen opens a file read-only after rejecting symlinks and paths that
// escape the allowed root. Inputs are never modified by the kernel.
//
// root may be "" to skip the containment check (used only by callers that
// have already resolved a trusted absolute path).
func SafeOpen(root, name string) (*os.File, error) {
	target := name
	if root != "" {
		resolved, err := resolveContained(root, name)
		if err != nil {
			return nil, err
		}
		target = resolved
	}
	if err := rejectSymlink(target); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(target, os.O_RDONLY, 0)
	if err != nil {
		return nil, NewError(StageScan, "safe.open", err.Error())
	}
	return f, nil
}

// resolveContained ensures name resolves inside root after cleaning both
// paths. A relative name is evaluated against root; the returned path is
// always absolute so callers open exactly the file that was validated.
func resolveContained(root, name string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", NewError(StageScan, "safe.path", err.Error())
	}
	target := name
	if !filepath.IsAbs(target) {
		target = filepath.Join(absRoot, target)
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", NewError(StageScan, "safe.path", err.Error())
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", NewError(StageScan, "safe.path",
			"path escapes input root: "+name).WithOffset(-1)
	}
	return target, nil
}

// rejectSymlink refuses a path whose final component is a symbolic link.
// Containment is enforced separately via checkContained, and fixtures are
// materialized inside the input root, so an in-root symlink is caught here.
func rejectSymlink(name string) error {
	fi, err := os.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return NewError(StageScan, "safe.lstat", err.Error())
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return NewError(StageScan, "safe.symlink",
			"symbolic links are not permitted: "+name)
	}
	return nil
}
