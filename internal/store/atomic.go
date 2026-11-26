// Package store publishes artifacts atomically and verifies their hashes.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"medpipe/internal/apperr"
)

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashFile streams a file and returns its hash and length.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, apperr.New("E_ARTIFACT_READ", "open %s: %v", path, err).WithStage("store")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, apperr.New("E_ARTIFACT_READ", "hash %s: %v", path, err).WithStage("store")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// WriteAtomic writes data to a same-directory temporary file, fsyncs it and
// renames it over path.  Readers therefore observe either the old or new
// complete file, never a partial write.
func WriteAtomic(path string, data []byte) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", apperr.New("E_ARTIFACT_WRITE", "create directory %s: %v", dir, err).WithStage("store")
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", apperr.New("E_ARTIFACT_WRITE", "create temp for %s: %v", path, err).WithStage("store")
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	n, err := w.Write(data)
	if err != nil {
		_ = tmp.Close()
		return "", apperr.New("E_ARTIFACT_WRITE", "write %s: %v", tmpName, err).WithStage("store")
	}
	if n != len(data) {
		_ = tmp.Close()
		return "", apperr.New("E_ARTIFACT_WRITE", "short write %s: %d/%d", tmpName, n, len(data)).WithStage("store")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", apperr.New("E_ARTIFACT_WRITE", "fsync %s: %v", tmpName, err).WithStage("store")
	}
	if err := tmp.Close(); err != nil {
		return "", apperr.New("E_ARTIFACT_WRITE", "close %s: %v", tmpName, err).WithStage("store")
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", apperr.New("E_ARTIFACT_WRITE", "chmod %s: %v", tmpName, err).WithStage("store")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", apperr.New("E_ARTIFACT_WRITE", "rename %s -> %s: %v", tmpName, path, err).WithStage("store")
	}
	cleanup = false
	if err := syncDir(dir); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Verify checks that path exists, has the expected length when wantSize >= 0,
// and has the expected SHA-256 when wantSHA is non-empty.
func Verify(path, wantSHA string, wantSize int64) (string, int64, error) {
	gotSHA, size, err := HashFile(path)
	if err != nil {
		return "", 0, err
	}
	if wantSize >= 0 && size != wantSize {
		return gotSHA, size, apperr.New("E_ARTIFACT_HASH", "size mismatch for %s: got %d want %d", path, size, wantSize).
			WithStage("store").WithRecoverable(true)
	}
	if wantSHA != "" && gotSHA != wantSHA {
		return gotSHA, size, apperr.New("E_ARTIFACT_HASH", "sha256 mismatch for %s", path).
			WithStage("store").WithRecoverable(true)
	}
	return gotSHA, size, nil
}

// Exists reports whether a path is a readable regular file.
func Exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// IsNotExist wraps errors.Is for callers.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return apperr.New("E_ARTIFACT_WRITE", "open directory %s: %v", dir, err).WithStage("store")
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return apperr.New("E_ARTIFACT_WRITE", "sync directory %s: %v", dir, err).WithStage("store")
	}
	return nil
}
