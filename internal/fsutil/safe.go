// Package fsutil centralizes the local-only filesystem rules: outputs stay
// inside an explicitly allowed root, path traversal is rejected and every
// publish goes through a temp file plus atomic rename.
package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape indicates a requested path resolves outside its allowed root.
var ErrPathEscape = errors.New("path escapes allowed root directory")

// SafeJoin joins root with name components and verifies the cleaned result
// remains within root. Name components must be plain basenames: separators
// and parent references are rejected outright.
func SafeJoin(root string, names ...string) (string, error) {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if name == "" || name == "." || name == ".." ||
			strings.ContainsAny(name, `/\`) ||
			filepath.IsAbs(name) {
			return "", &PathError{Name: name, Root: cleanRoot}
		}
	}
	target := cleanRoot
	for _, name := range names {
		target = filepath.Join(target, name)
	}
	// Defense in depth: even after lexical checks, confirm containment.
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &PathError{Name: filepath.Join(names...), Root: cleanRoot}
	}
	return target, nil
}

// PathError carries the offending name for stable error messages.
type PathError struct {
	Name string
	Root string
}

func (e *PathError) Error() string {
	return ErrPathEscape.Error() + ": " + e.Name + " under " + e.Root
}

// Unwrap exposes the sentinel so errors.Is(err, ErrPathEscape) works.
func (e *PathError) Unwrap() error { return ErrPathEscape }

// SafeJoinRel joins root with a slash-separated relative path such as a
// playlist location ("rendition_123/playlist.m3u8"). Every element is
// validated and the cleaned result must stay inside root.
func SafeJoinRel(root, rel string) (string, error) {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		return "", &PathError{Name: rel, Root: cleanRoot}
	}
	elements := strings.FieldsFunc(rel, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	})
	target := cleanRoot
	for _, elem := range elements {
		if elem == "" || elem == "." || elem == ".." {
			return "", &PathError{Name: rel, Root: cleanRoot}
		}
		target = filepath.Join(target, elem)
	}
	relCheck, err := filepath.Rel(cleanRoot, target)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", &PathError{Name: rel, Root: cleanRoot}
	}
	return target, nil
}

// IsPathEscape reports whether err wraps ErrPathEscape.
func IsPathEscape(err error) bool { return errors.Is(err, ErrPathEscape) }

// AtomicWriteFile writes data to a sibling temp file and renames it into
// place, so readers never observe a partially written manifest or segment.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
