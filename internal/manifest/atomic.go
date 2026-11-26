package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteAtomic encodes and atomically renames a manifest into path. Concurrent
// deliveries using the same path either observe the old or new complete file,
// never a partially written JSON document.
func WriteAtomic(path string, m *Manifest, perm fs.FileMode) (string, []byte, error) {
	b, sum, err := Encode(m)
	if err != nil {
		return "", nil, err
	}
	if perm == 0 {
		perm = 0o644
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.json.tmp")
	if err != nil {
		return "", nil, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return "", nil, err
	}
	return sum, b, nil
}

func SHA256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
