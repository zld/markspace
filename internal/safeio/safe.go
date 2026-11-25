// Package safeio confines all file output to a caller-provided local
// directory. It rejects path traversal and symlink escapes, and writes
// checkpoints and segments through a temp-file-plus-fsync-plus-rename
// sequence so readers never observe a half-written file.
package safeio

import (
	"os"
	"path/filepath"
	"strings"

	"mediad/internal/fault"
)

// SafeJoin resolves name against root and guarantees the result stays inside
// root. Every path component is inspected for symlinks; a link pointing
// outside the root is refused rather than followed.
func SafeJoin(root, name string) (string, error) {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fault.Wrap(fault.CatUnsafePath, "safeio.join", -1, -1, true, err)
	}
	if filepath.IsAbs(name) {
		return "", fault.New(fault.CatUnsafePath, "safeio.join", true,
			"absolute output paths are not allowed")
	}
	target := filepath.Clean(filepath.Join(cleanRoot, name))
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fault.New(fault.CatUnsafePath, "safeio.join", true,
			"path escapes the output directory: "+name)
	}
	// Walk existing ancestors and reject symlink escapes.
	cur := cleanRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue // not created yet; the final component is normal
		}
		if err != nil {
			return "", fault.Wrap(fault.CatUnsafePath, "safeio.join", -1, -1, true, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fault.New(fault.CatUnsafePath, "safeio.join", true,
				"symlinks inside the output directory are not allowed: "+cur)
		}
	}
	return target, nil
}

// EnsureRoot creates root (mode 0o755) when missing and validates it is a
// real directory rather than a symlink.
func EnsureRoot(root string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fault.Wrap(fault.CatUnsafePath, "safeio.root", -1, -1, true, err)
	}
	info, err := os.Lstat(abs)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", fault.Wrap(fault.CatWrite, "safeio.root", -1, -1, true, err)
		}
	case err != nil:
		return "", fault.Wrap(fault.CatWrite, "safeio.root", -1, -1, true, err)
	default:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fault.New(fault.CatUnsafePath, "safeio.root", true,
				"output root must not be a symlink")
		}
		if !info.IsDir() {
			return "", fault.New(fault.CatUnsafePath, "safeio.root", true,
				"output root is not a directory")
		}
	}
	return abs, nil
}

// AtomicWrite writes data to a sibling temp file, fsyncs it, then renames it
// over dst. Either the previous complete file or the new complete file is
// visible at dst — never a partial write.
func AtomicWrite(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return fault.Wrap(fault.CatWrite, "safeio.atomic", -1, -1, true, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fault.Wrap(fault.CatWrite, "safeio.atomic", -1, -1, true, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fault.Wrap(fault.CatWrite, "safeio.atomic", -1, -1, true, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fault.Wrap(fault.CatWrite, "safeio.atomic", -1, -1, true, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return fault.Wrap(fault.CatWrite, "safeio.atomic", -1, -1, true, err)
	}
	// fsync the directory so the rename itself is durable.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
