package medpipe

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveLocal joins name onto base and guarantees the result stays inside
// base. Absolute names, parent traversal and symlink escapes are rejected so
// index/manifest writes can never land outside the pipeline work directory.
func ResolveLocal(base, name string) (string, *PipelineError) {
	if filepath.IsAbs(name) {
		return "", NewErrorBuilder(name).Fatal(CodePathDenied,
			"absolute paths are not allowed for pipeline outputs",
			"pass a path relative to the run directory")
	}
	cleanBase, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", NewErrorBuilder(name).Fatal(CodePathDenied,
			"cannot resolve run directory: "+err.Error(),
			"check the --input/working directory path")
	}
	joined := filepath.Clean(filepath.Join(cleanBase, name))
	rel, err := filepath.Rel(cleanBase, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", NewErrorBuilder(name).Fatal(CodePathDenied,
			"path escapes the run directory: "+name,
			"remove parent-directory (..) components from the path")
	}
	// Reject symlink escapes on existing path components.
	if _, err := os.Stat(joined); err == nil {
		real, err := filepath.EvalSymlinks(joined)
		if err != nil {
			return "", NewErrorBuilder(name).Fatal(CodePathDenied,
				"cannot evaluate path: "+err.Error(), "replace symlinks with regular files")
		}
		realBase, err := filepath.EvalSymlinks(cleanBase)
		if err == nil {
			cleanBase = realBase
		}
		if rel, err := filepath.Rel(cleanBase, real); err != nil || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", NewErrorBuilder(name).Fatal(CodePathDenied,
				"symlink resolves outside the run directory: "+name,
				"remove the symlink or move the target inside the run directory")
		}
	}
	return joined, nil
}

// RequireLocalFile validates that path is a regular local file (not a device,
// pipe, directory or symlink). Pipeline inputs are restricted to local files
// per the engineering constraints; absolute local paths are accepted, while
// all output writes are additionally confined by ResolveLocal.
func RequireLocalFile(path string) *PipelineError {
	info, err := os.Lstat(path)
	if err != nil {
		return NewErrorBuilder(path).Fatal(CodePathDenied,
			"cannot stat input: "+err.Error(), "check the --input path")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return NewErrorBuilder(path).Fatal(CodePathDenied,
			"symlinked inputs are not allowed", "replace the symlink with a regular file")
	}
	if !info.Mode().IsRegular() {
		return NewErrorBuilder(path).Fatal(CodePathDenied,
			"input is not a regular file", "provide a local capture file")
	}
	return nil
}

// AtomicWriteFile writes data to name via a temp file in the same directory
// followed by an atomic rename, so interrupted runs never leave a partial
// index or manifest that a later run could mistake for a complete one.
func AtomicWriteFile(name string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".medpipe-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, name)
}
