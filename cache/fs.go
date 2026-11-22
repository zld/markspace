package cache

import (
	"os"
	"path/filepath"

	"mediamux/media"
)

// writeFileAtomic writes b to path via a temp file in the same directory and
// a rename, so a crash can never leave a partially written file behind.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCache,
			"create cache directory").AtOffset(0).Because(err)
	}
	tmp, err := os.CreateTemp(dir, ".cas-*")
	if err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCache,
			"create temporary cache file").AtOffset(0).Because(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return media.NewError(media.CodeInternal, media.PhaseCache,
			"write temporary cache file").AtOffset(0).Because(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.NewError(media.CodeInternal, media.PhaseCache,
			"sync temporary cache file").AtOffset(0).Because(err)
	}
	if err := tmp.Close(); err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCache,
			"close temporary cache file").AtOffset(0).Because(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCache,
			"commit cache file").AtOffset(0).Because(err)
	}
	return nil
}
