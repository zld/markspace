package manifest

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafeResolve resolves ref relative to root and guarantees the result stays
// inside root. Absolute paths, URI schemes and ".." escapes are rejected.
func SafeResolve(root, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty reference")
	}
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "file:") {
		return "", fmt.Errorf("refusing non-local reference %q", ref)
	}
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") {
		return "", fmt.Errorf("absolute reference %q outside material directory", ref)
	}
	// Strip any query/fragment just in case.
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(ref)))
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reference %q escapes material directory", ref)
	}
	return target, nil
}

// RelativeURI returns target relative to root with forward slashes for stable
// reports. It falls back to filepath.Base when relativization fails.
func RelativeURI(root, target string) string {
	if rel, err := filepath.Rel(root, target); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(target)
}
