// Package workspace enforces that all local fixture and processing paths stay under --work.
package workspace

import (
	"path/filepath"
	"strings"

	"mediacore/internal/diagnostic"
)

// Resolve joins a manifest-relative reference and verifies the result is inside root.
func Resolve(root, manifestDir, ref string) (string, error) {
	if filepath.IsAbs(ref) {
		return "", diagnostic.Wrap(diagnostic.ErrPathEscape, diagnostic.Location{Stage: "path"}, "absolute media references are not allowed: "+ref)
	}
	cleanRef := filepath.Clean(filepath.FromSlash(ref))
	if cleanRef == ".." || strings.HasPrefix(cleanRef, ".."+string(filepath.Separator)) {
		return "", diagnostic.Wrap(diagnostic.ErrPathEscape, diagnostic.Location{Stage: "path"}, "media reference escapes manifest directory: "+ref)
	}
	base := manifestDir
	if base == "" {
		base = root
	}
	p := filepath.Clean(filepath.Join(base, cleanRef))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absP)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", diagnostic.Wrap(diagnostic.ErrPathEscape, diagnostic.Location{Stage: "path"}, "path is outside work directory: "+ref)
	}
	return absP, nil
}
