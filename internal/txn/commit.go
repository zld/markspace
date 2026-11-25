package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Artifact is one immutable output file with its content hash.
type Artifact struct {
	Path   string `json:"path"` // relative to the stage output directory
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the immutable, hash-verified record of a committed stage.
type Manifest struct {
	Stage     string     `json:"stage"`
	Artifacts []Artifact `json:"artifacts"`
}

// Manager coordinates one stage at a time over a working directory:
// <dir>/wal.log, <dir>/staging/<stage>/ for uncommitted work, and
// <dir>/committed/<stage>/ for durable, immutable artifacts plus manifest.
type Manager struct {
	dir string
	wal *WAL
}

// NewManager opens (or creates) a transaction environment rooted at dir.
func NewManager(dir string) (*Manager, error) {
	wal, err := OpenWAL(dir)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"staging", "committed"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			wal.Close()
			return nil, err
		}
	}
	return &Manager{dir: dir, wal: wal}, nil
}

// WAL exposes the journal for callers that need to append stage records.
func (m *Manager) WAL() *WAL { return m.wal }

// Close releases the journal.
func (m *Manager) Close() error { return m.wal.Close() }

// StagingDir returns the private working directory for a stage, creating it.
// Callers write artifacts here; nothing here is visible as committed output.
func (m *Manager) StagingDir(stage string) (string, error) {
	d := filepath.Join(m.dir, "staging", stage)
	return d, os.MkdirAll(d, 0o755)
}

// WriteStaging writes one artifact file under the stage staging directory.
func (m *Manager) WriteStaging(stage, name string, data []byte) (string, error) {
	d, err := m.StagingDir(stage)
	if err != nil {
		return "", err
	}
	target := filepath.Join(d, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	return target, nil
}

// Begin records the stage intent in the WAL.
func (m *Manager) Begin(stage string) error {
	d, err := m.StagingDir(stage)
	if err != nil {
		return err
	}
	return m.wal.Begin(stage, d)
}

// Commit hashes every staging artifact, writes the manifest atomically,
// moves the whole staging directory to committed, and records a commit in
// the WAL. After return the artifacts are durable and immutable by contract.
func (m *Manager) Commit(stage string) (*Manifest, error) {
	staging := filepath.Join(m.dir, "staging", stage)
	manifest, err := buildManifest(stage, staging)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	// Manifest lands inside staging first, then moves with the directory.
	mp := filepath.Join(staging, "manifest.json")
	if err := atomicWrite(mp, manifestBytes); err != nil {
		return nil, err
	}
	committed := filepath.Join(m.dir, "committed", stage)
	if err := os.RemoveAll(committed); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, committed); err != nil {
		return nil, err
	}
	if err := m.wal.Commit(stage); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Rollback discards a stage's uncommitted staging files and records an abort
// in the WAL. It is safe to call when nothing was staged.
func (m *Manager) Rollback(stage string) error {
	if err := os.RemoveAll(filepath.Join(m.dir, "staging", stage)); err != nil {
		return err
	}
	return m.wal.Abort(stage)
}

// CommittedDir returns the immutable artifact directory for a stage.
func (m *Manager) CommittedDir(stage string) string {
	return filepath.Join(m.dir, "committed", stage)
}

// ReadManifest loads and returns a committed stage's manifest.
func (m *Manager) ReadManifest(stage string) (*Manifest, error) {
	mp := filepath.Join(m.CommittedDir(stage), "manifest.json")
	data, err := os.ReadFile(mp)
	if err != nil {
		return nil, fmt.Errorf("txn: read manifest %s: %w", stage, err)
	}
	var man Manifest
	if err := json.Unmarshal(data, &man); err != nil {
		return nil, err
	}
	return &man, nil
}

// VerifyManifest recomputes every artifact hash and compares it to the
// manifest, returning an error on any mismatch or missing file.
func (m *Manager) VerifyManifest(stage string) error {
	man, err := m.ReadManifest(stage)
	if err != nil {
		return err
	}
	base := m.CommittedDir(stage)
	for _, a := range man.Artifacts {
		got, size, err := hashFile(filepath.Join(base, a.Path))
		if err != nil {
			return fmt.Errorf("txn: verify %s/%s: %w", stage, a.Path, err)
		}
		if got != a.SHA256 {
			return fmt.Errorf("txn: hash mismatch for %s/%s: manifest=%s actual=%s",
				stage, a.Path, a.SHA256, got)
		}
		if size != a.Size {
			return fmt.Errorf("txn: size mismatch for %s/%s", stage, a.Path)
		}
	}
	return nil
}

// Recover reconciles on-disk state with the WAL after a restart. Committed
// stages are left intact and verified; stages begun but not committed have
// their staging directories removed and an abort logged, so no half-finished
// artifacts survive. It returns the ordered list of durably committed stages.
func (m *Manager) Recover() ([]string, error) {
	states, err := m.wal.States()
	if err != nil {
		return nil, err
	}
	var committed []string
	for _, stage := range Stages {
		st, known := states[stage]
		committedDir := filepath.Join(m.dir, "committed", stage)
		switch {
		case known && st.Committed:
			if err := m.VerifyManifest(stage); err != nil {
				return committed, err
			}
			committed = append(committed, stage)
		default:
			// Begun-but-uncommitted, or orphaned staging on disk: clean it.
			if err := os.RemoveAll(filepath.Join(m.dir, "staging", stage)); err != nil {
				return committed, err
			}
			// A committed directory without a WAL commit is also untrusted.
			if _, err := os.Stat(committedDir); err == nil && !(known && st.Committed) {
				if err := os.RemoveAll(committedDir); err != nil {
					return committed, err
				}
			}
			if known && st.Begun && !st.Committed {
				if err := m.wal.Abort(stage); err != nil {
					return committed, err
				}
			}
		}
	}
	return committed, nil
}

func buildManifest(stage, dir string) (*Manifest, error) {
	man := &Manifest{Stage: stage}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Base(path) == "manifest.json" {
			return nil
		}
		hash, size, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		man.Artifacts = append(man.Artifacts, Artifact{
			Path: filepath.ToSlash(rel), Size: size, SHA256: hash,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return man, nil
}

func hashFile(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
