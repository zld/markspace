package pipeline

import (
	"os"
	"path/filepath"

	"mediamerge/internal/txn"
)

// readSegment loads a promoted segment payload from the run directory.
func readSegment(outDir string, seg txn.Segment) ([]byte, error) {
	path := filepath.Join(outDir, "txn", filepath.FromSlash(seg.Path))
	return os.ReadFile(path)
}
