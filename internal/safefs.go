package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxCheckpointBytes bounds recovery metadata in one process/file.
	MaxCheckpointBytes = 256 << 10
	// MaxManifestBytes bounds manifest and cache files.
	MaxManifestBytes = 1 << 20
)

// SafeJoin resolves name under root and rejects absolute or traversal paths.
func SafeJoin(root, name string) (string, error) {
	if name == "" {
		return "", &MediaError{Stage: "path", Code: "empty_path", Message: "empty relative path", Recoverable: false}
	}
	if filepath.IsAbs(name) || strings.Contains(name, "\x00") {
		return "", &MediaError{Stage: "path", Code: "unsafe_path", Message: fmt.Sprintf("absolute or invalid path %q", name), Recoverable: false}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", &MediaError{Stage: "path", Code: "path_traversal", Message: fmt.Sprintf("path %q escapes output root", name), Recoverable: false}
	}
	target := filepath.Join(rootAbs, clean)
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", &MediaError{Stage: "path", Code: "path_traversal", Message: fmt.Sprintf("path %q escapes output root", name), Recoverable: false}
	}
	return target, nil
}

// WriteFileAtomic writes data via a same-directory temp file and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// WriteJSONAtomic enforces size and writes indented deterministic JSON.
func WriteJSONAtomic(path string, v any, limit int) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > limit {
		return &MediaError{
			Stage:       "checkpoint",
			Code:        "artifact_too_large",
			Message:     fmt.Sprintf("JSON size %d exceeds limit %d", len(data), limit),
			Recoverable: false,
		}
	}
	data = append(data, '\n')
	return WriteFileAtomic(path, data, 0o644)
}

// ReadJSONLimited reads a bounded JSON file.
func ReadJSONLimited(path string, limit int, v any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > int64(limit) {
		return &MediaError{
			Stage:       "checkpoint",
			Code:        "artifact_too_large",
			Message:     fmt.Sprintf("file size %d exceeds limit %d", info.Size(), limit),
			Recoverable: false,
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return &MediaError{Stage: "checkpoint", Code: "artifact_too_large", Recoverable: false}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
