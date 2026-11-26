package core

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// SafeJoin joins a root-relative, slash-separated path onto root and verifies
// the result stays inside root. Both the manifest and the media scanners use
// it, so every file access shares one containment rule.
func SafeJoin(root, rel string) (string, error) {
	clean := path.Clean(rel)
	if rel == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("path %q escapes media root", rel)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(clean)))
	if err != nil {
		return "", err
	}
	within, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	if within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes media root", rel)
	}
	return abs, nil
}
