package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path using a temporary file in the same
// directory followed by a rename. If any step fails, the temporary file is
// removed and an existing file at path is left untouched.
func WriteFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary manifest: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary manifest: %w", err)
	}
	if err = tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("set manifest permissions: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temporary manifest: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	// Best effort: make the rename durable on the directory.
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// WriteTempError reports whether err came from the temporary-file stage, so
// callers can distinguish a missing/unwritable directory from a failed
// replacement.
func WriteTempError(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}
