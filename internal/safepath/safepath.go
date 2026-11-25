// Package safepath confines all generated and consumed files to a working directory.
package safepath

import (
	"path/filepath"
	"strings"

	"mediaflow/internal/fault"
)

// Resolve joins parts under root and rejects absolute paths or traversal escaping root.
func Resolve(root string, parts ...string) (string, error) {
	if root == "" {
		return "", fault.New(fault.ClassPath, "working directory is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fault.Wrap(fault.ClassPath, err, "cannot resolve working directory")
	}
	cleanParts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			return "", fault.New(fault.ClassPath, "absolute path %q is not allowed", p)
		}
		cleanParts = append(cleanParts, filepath.FromSlash(p))
	}
	target := filepath.Join(append([]string{absRoot}, cleanParts...)...)
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", fault.Wrap(fault.ClassPath, err, "cannot check path containment")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fault.New(fault.ClassPath, "path escapes working directory: %s", rel)
	}
	return target, nil
}

// CheckWithin resolves an OS-supplied path against the current directory and
// ensures it is the root itself or beneath root.
func CheckWithin(root, p string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fault.Wrap(fault.ClassPath, err, "cannot resolve working directory")
	}
	var absTarget string
	if filepath.IsAbs(p) {
		absTarget = filepath.Clean(p)
	} else {
		absTarget, err = filepath.Abs(p)
		if err != nil {
			return "", fault.Wrap(fault.ClassPath, err, "cannot resolve path")
		}
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return "", fault.Wrap(fault.ClassPath, err, "cannot check path containment")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fault.New(fault.ClassPath, "path escapes working directory: %s", p)
	}
	return absTarget, nil
}
