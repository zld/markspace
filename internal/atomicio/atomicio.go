// Package atomicio writes output through a same-directory temporary file and
// renames it into place, so a failed or partial write can never leave a
// half-written target behind.
package atomicio

import (
	"io"
	"os"
	"path/filepath"
)

// limits enforced by the pipeline (see README / cli validation).
const (
	MaxInputBytes = 64 * 1024 * 1024
	MaxCues       = 200_000
)

// WriteFile writes data to a temp file next to target, fsyncs it, and
// atomically renames it over target. The temp file is removed on any error.
func WriteFile(target string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".subsync-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err = tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err = tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err = tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err = os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// ReadLimited reads at most MaxInputBytes+1 bytes so callers can distinguish
// an over-limit file from a valid one.
func ReadLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxInputBytes+1))
}
