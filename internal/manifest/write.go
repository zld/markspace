package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zld/media-check/internal/fault"
)

// Encode returns the stable manifest bytes. Callers can hash this before
// writing or compare across resumed runs.
func Encode(m *Manifest) ([]byte, string, error) {
	m = Merge(nil, m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, "", fault.Wrap(fault.StageManifest, fault.CodeManifestMismatch, err, "encode manifest")
	}
	b = append(b, '\n')
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

// WriteFile atomically writes manifest.json and a sibling SHA-256 file.
func WriteFile(path string, m *Manifest) (string, error) {
	b, sum, err := Encode(m)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "create manifest directory",
			fault.WithPath(path))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return "", fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "create temporary manifest")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return "", fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "write manifest")
	}
	if err := tmp.Close(); err != nil {
		return "", fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "close manifest")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "replace manifest")
	}
	shaPath := path + ".sha256"
	if err := os.WriteFile(shaPath, []byte(sum+"  "+filepath.Base(path)+"\n"), 0o644); err != nil {
		return "", fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "write manifest checksum")
	}
	return sum, nil
}

// Archive writes both the stable task-keyed copy and the work-root manifest.
func Archive(workDir string, m *Manifest) (rootPath, archivePath, sum string, err error) {
	rootPath = filepath.Join(workDir, "manifest.json")
	archivePath = filepath.Join(workDir, "archive", m.TaskKey, "manifest.json")
	if _, err := WriteFile(archivePath, m); err != nil {
		return "", "", "", err
	}
	sum, err = WriteFile(rootPath, m)
	if err != nil {
		return "", "", "", err
	}
	return rootPath, archivePath, sum, nil
}

// Load reads and decodes a manifest. A missing file is not an error when
// ok=false is returned.
func Load(path string) (*Manifest, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fault.Wrap(fault.StageManifest, fault.CodeReadFailed, err, "read manifest",
			fault.WithPath(path))
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false, fault.Wrap(fault.StageManifest, fault.CodeManifestMismatch, err, "decode manifest",
			fault.WithPath(path))
	}
	return &m, true, nil
}
