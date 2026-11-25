package provenance

import (
	"os"
	"path/filepath"
	"strconv"
)

// writeAtomic writes data to path via a temp file and rename, so a crash can
// never leave a half-written trace.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trace-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
