package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"streamcheck/internal/manifest"
)

// SafeJoin resolves a playlist entry below dir and rejects absolute paths,
// separators and parent references so a manifest cannot point outside the
// input directory.
func SafeJoin(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid file name")
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("file name must not contain a path")
	}
	full := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file name escapes input directory")
	}
	return full, nil
}

// CheckUnchanged re-stats every scanned file and reports size or modification
// time changes since the scan.
func CheckUnchanged(segments []manifest.Segment) []string {
	var issues []string
	for _, seg := range segments {
		fi, err := os.Stat(seg.Path)
		if err != nil {
			issues = append(issues, fmt.Sprintf("changed_file %s: %v", seg.FileName, err))
			continue
		}
		var changes []string
		if fi.Size() != seg.SizeBytes {
			changes = append(changes, fmt.Sprintf("size %d, was %d", fi.Size(), seg.SizeBytes))
		}
		if fi.ModTime().UnixNano() != seg.ModTimeUnixNano {
			changes = append(changes, "modification time changed")
		}
		if len(changes) > 0 {
			issues = append(issues, fmt.Sprintf("changed_file %s: %s", seg.FileName, strings.Join(changes, "; ")))
		}
	}
	return issues
}
