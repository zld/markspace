// Package pathsafe confines generated index, manifest and media paths to the
// output directory named on the command line.
package pathsafe

import (
	"path/filepath"
	"strings"

	"mediapack/internal/fault"
)

// Join joins elements under root and rejects any result outside root.
func Join(root string, elems ...string) (string, error) {
	for _, e := range elems {
		if filepath.IsAbs(e) {
			return "", fault.AtPosition(fault.KindPath, e, "absolute child path is not allowed")
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fault.Wrap(fault.KindPath, err)
	}
	joined := filepath.Join(append([]string{absRoot}, elems...)...)
	rel, err := filepath.Rel(absRoot, joined)
	if err != nil {
		return "", fault.Wrap(fault.KindPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fault.AtPosition(fault.KindPath, joined, "path escapes output root")
	}
	return joined, nil
}

// Check reports whether target is at or below root after lexical cleaning.
func Check(root, target string) error {
	_, err := Join(root, target)
	return err
}
