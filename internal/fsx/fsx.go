package fsx

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediarepair/internal/media"
)

// SafeJoin joins root and a manifest-relative path and rejects escapes.
func SafeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", media.E(media.CodePathEscape, "fsx.safe_join", errors.New("empty path"))
	}
	if filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return "", media.E(media.CodePathEscape, "fsx.safe_join", errors.New("absolute or invalid path")).WithFile(rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", media.E(media.CodePathEscape, "fsx.safe_join", errors.New("path escapes root")).WithFile(rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", media.E(media.CodeWrite, "fsx.abs_root", err).WithFile(root)
	}
	full := filepath.Join(absRoot, clean)
	relToRoot, err := filepath.Rel(absRoot, full)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", media.E(media.CodePathEscape, "fsx.safe_join", err).WithFile(rel)
	}
	return full, nil
}

// AtomicWriteFile writes through a temp file in the same directory, fsyncs
// the file, renames it into place, and fsyncs the directory.
func AtomicWriteFile(path string, r io.Reader, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.E(media.CodeWrite, "fsx.mkdir", err).WithFile(dir)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return media.E(media.CodeWrite, "fsx.temp_create", err).WithFile(path)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }

	if _, err := io.Copy(tmp, r); err != nil {
		cleanup()
		return media.E(media.CodeWrite, "fsx.temp_write", err).WithFile(path)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return media.E(media.CodeWrite, "fsx.temp_sync", err).WithFile(path)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return media.E(media.CodeWrite, "fsx.chmod", err).WithFile(path)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return media.E(media.CodeWrite, "fsx.close", err).WithFile(path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return media.E(media.CodeWrite, "fsx.rename", err).WithFile(path)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// WriteSynced stages data at a caller-selected temporary path and syncs it.
// CommitStage then atomically renames the staged file.
func WriteSynced(stagePath string, r io.Reader, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
		return media.E(media.CodeWrite, "fsx.stage_mkdir", err).WithFile(stagePath)
	}
	f, err := os.OpenFile(stagePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return media.E(media.CodeWrite, "fsx.stage_open", err).WithFile(stagePath)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return media.E(media.CodeWrite, "fsx.stage_write", err).WithFile(stagePath)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return media.E(media.CodeWrite, "fsx.stage_sync", err).WithFile(stagePath)
	}
	return f.Close()
}

func CommitStage(stagePath, finalPath string) error {
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return media.E(media.CodeWrite, "fsx.commit_mkdir", err).WithFile(finalPath)
	}
	if err := os.Rename(stagePath, finalPath); err != nil {
		return media.E(media.CodeWrite, "fsx.commit_rename", err).WithFile(finalPath)
	}
	if d, err := os.Open(filepath.Dir(finalPath)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
