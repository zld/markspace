package index

import (
	"errors"
	"os"
	"path/filepath"

	"mediad/internal/fault"
	"mediad/internal/safeio"
)

// ErrNotFound signals that no journal exists yet in the output directory.
var ErrNotFound = errors.New("index: journal not found")

func newCorrupt(cause error) error {
	return fault.Wrap(fault.CatCheckpoint, "index.load", -1, -1, true, cause)
}

// SaveSnapshot writes a numbered, durable copy of the journal alongside the
// live index. Snapshots let recovery roll back to a known-good checkpoint
// when the latest journal is truncated or tampered with.
func (ix *Index) SaveSnapshot(root string, n int) error {
	if _, err := safeioEnsureSnapDir(root); err != nil {
		return err
	}
	dst, err := SnapshotPath(root, n)
	if err != nil {
		return err
	}
	data, err := jsonMarshal(ix)
	if err != nil {
		return err
	}
	return safeioAtomic(dst, data)
}

// LatestSnapshot returns the highest-numbered snapshot index and path, or
// ErrNotFound when no snapshots exist.
func LatestSnapshot(root string) (int, string, error) {
	dir, err := safeio.SafeJoin(root, "snapshots")
	if err != nil {
		return 0, "", err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", err
	}
	best := -1
	var bestPath string
	for _, e := range entries {
		var n int
		if _, err := parseSnapshotName(e.Name(), &n); err != nil {
			continue // ignore unrelated files
		}
		if n > best {
			best = n
			bestPath = filepath.Join(dir, e.Name())
		}
	}
	if best < 0 {
		return 0, "", ErrNotFound
	}
	return best, bestPath, nil
}
