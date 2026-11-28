package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediapack/internal/model"
)

// SafePath joins name onto root and guarantees the result stays inside root.
// Every on-disk path in the package is built through this guard so a crafted
// layer/file name cannot escape the output directory.
func SafePath(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) ||
		clean == ".." || strings.Contains(clean, ".."+string(os.PathSeparator)) {
		return "", model.NewFieldError(model.KindInvalidInput, root, -1,
			"path", "path escapes output directory: "+name)
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", model.NewFieldError(model.KindInvalidInput, root, -1,
			"path", "path escapes output directory: "+name)
	}
	return full, nil
}

// atomicWrite writes data to a sibling temp file then renames it into place,
// so a crash never leaves a half-written artifact behind.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Write persists the manifest and its SHA-256 sidecar atomically.
func Write(root string, m *Manifest) (string, error) {
	raw, err := CanonicalBytes(m)
	if err != nil {
		return "", err
	}
	mPath, err := SafePath(root, FileManifest)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(mPath, raw, 0o644); err != nil {
		return "", err
	}
	sumPath, err := SafePath(root, FileChecksum)
	if err != nil {
		return "", err
	}
	checksum := HashBytes(raw)
	if err := atomicWrite(sumPath, []byte(checksum+"\n"), 0o644); err != nil {
		return "", err
	}
	return checksum, nil
}

// Read loads and validates the manifest against its checksum sidecar.
func Read(root string) (*Manifest, []byte, string, error) {
	mPath, err := SafePath(root, FileManifest)
	if err != nil {
		return nil, nil, "", err
	}
	raw, err := os.ReadFile(mPath)
	if err != nil {
		return nil, nil, "", model.WrapField(model.KindInvalidInput, mPath, -1,
			FileManifest, "cannot read manifest", err)
	}
	sumPath, err := SafePath(root, FileChecksum)
	if err != nil {
		return nil, nil, "", err
	}
	sidecar, err := os.ReadFile(sumPath)
	if err != nil {
		return nil, nil, "", model.WrapField(model.KindChecksum, sumPath, -1,
			FileChecksum, "cannot read checksum sidecar", err)
	}
	want := strings.TrimSpace(string(sidecar))
	got := HashBytes(raw)
	if want != got {
		return nil, nil, "", model.NewFieldError(model.KindChecksum, sumPath, -1,
			FileChecksum, "manifest checksum mismatch")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, "", model.WrapField(model.KindInvalidInput, mPath, -1,
			FileManifest, "manifest is not valid JSON", err)
	}
	return &m, raw, got, nil
}

// FrameRecordPath returns the per-layer frame record file name.
func FrameRecordPath(layer string) string {
	return filepath.Join(DirLayers, layer, "frames.json")
}

// WriteFrameRecords writes one layer's frame records atomically.
func WriteFrameRecords(root, layer string, frames []FrameEntry) error {
	path, err := SafePath(root, FrameRecordPath(layer))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(frames, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), 0o644)
}

// ReadFrameRecords loads one layer's frame records.
func ReadFrameRecords(root, layer string) ([]FrameEntry, error) {
	path, err := SafePath(root, FrameRecordPath(layer))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.WrapField(model.KindInvalidInput, path, -1,
			FrameRecordPath(layer), "cannot read frame records", err)
	}
	var frames []FrameEntry
	if err := json.Unmarshal(raw, &frames); err != nil {
		return nil, model.WrapField(model.KindInvalidInput, path, -1,
			FrameRecordPath(layer), "frame records are not valid JSON", err)
	}
	return frames, nil
}

// HashReader returns the hex SHA-256 of everything read from r.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ErrVerification is the sentinel callers can check for a failed verify.
var ErrVerification = errors.New("manifest verification failed")
