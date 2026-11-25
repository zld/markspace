package index

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"mediad/internal/safeio"
)

func safeioEnsureSnapDir(root string) (string, error) {
	dir, err := safeio.SafeJoin(root, "snapshots")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func jsonMarshal(ix *Index) ([]byte, error) {
	return json.MarshalIndent(ix, "", "  ")
}

func safeioAtomic(dst string, data []byte) error {
	return safeio.AtomicWrite(dst, data)
}

// parseSnapshotName parses "checkpoint-000123.json" into its sequence number.
func parseSnapshotName(name string, n *int) (bool, error) {
	const prefix, suffix = "checkpoint-", ".json"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false, errors.New("not a snapshot file")
	}
	mid := name[len(prefix) : len(name)-len(suffix)]
	v, err := strconv.Atoi(mid)
	if err != nil {
		return false, err
	}
	*n = v
	return true, nil
}
