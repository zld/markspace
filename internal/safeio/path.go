// Package safeio centralizes the filesystem safety rules used by the
// pipeline: outputs stay inside an allowed root and temporary files carry
// predictable, freshly-created names instead of attacker-controlled ones.
package safeio

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathRejected is returned when a path escapes its allowed root or
// contains unsafe name components.
var ErrPathRejected = errors.New("unsafe path rejected")

// ResolveUnder cleans p and guarantees it sits inside root (which itself is
// cleaned and evaluated). Symlinks in existing components are resolved so
// link-based escapes cannot slip past the prefix check.
func ResolveUnder(root, p string) (string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = real
	} else if !os.IsNotExist(err) {
		return "", err
	}
	target := p
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	target = filepath.Clean(target)
	// Resolve the existing prefix; only the final leaf may not exist yet.
	prefix := target
	for {
		real, err := filepath.EvalSymlinks(prefix)
		if err == nil {
			prefix = real
			break
		}
		if !os.IsNotExist(err) || prefix == filepath.Dir(prefix) {
			return "", err
		}
		prefix = filepath.Dir(prefix)
	}
	rel, err := filepath.Rel(rootAbs, prefix)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s escapes %s", ErrPathRejected, p, root)
	}
	return target, nil
}

// SafeName rejects empty names, absolute paths and any traversal or
// separator content; it also forbids names beginning with a dot so temp
// files cannot masquerade as hidden metadata.
func SafeName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\") || strings.Contains(name, "\x00") ||
		strings.HasPrefix(name, ".") {
		return fmt.Errorf("%w: %q", ErrPathRejected, name)
	}
	return nil
}

// TempFile creates a randomly named file (prefix + 16 hex chars + suffix)
// inside dir and returns it already open, along with its full path. The
// name is generated locally; no caller-supplied name is trusted.
func TempFile(dir, prefix, suffix string) (*os.File, string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, "", err
	}
	name := prefix + hex.EncodeToString(raw[:]) + suffix
	path := filepath.Join(filepath.Clean(dir), name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", err
	}
	return f, path, nil
}
