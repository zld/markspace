package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/journal"
)

// safeJoin resolves name relative to base and refuses absolute names or
// any result that escapes base through ".." traversal. Inputs stay inside
// the read-only input directory.
func safeJoin(base, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fault.New(fault.StageInput, "manifest references an empty or absolute path").
			At(name, -1)
	}
	p := filepath.Join(base, filepath.FromSlash(name))
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fault.New(fault.StageInput, "manifest reference escapes input directory").
			At(name, -1)
	}
	return p, nil
}

// readSegment reads one segment file and returns its content and digest.
func readSegment(inDir, name string) ([]byte, string, error) {
	path, err := safeJoin(inDir, name)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fault.New(fault.StageInput, "manifest references missing segment").
			At(path, -1).Wrap(err)
	}
	return data, journal.DigestBytes(data), nil
}

func absMillis(d clock.Ticks) int64 {
	if d < 0 {
		d = -d
	}
	return d.Millis()
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
