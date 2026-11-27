package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Point is the durable rollback record. It is written atomically after every
// stage so a crash always leaves a resumable state.
type Point struct {
	Stage        string `json:"stage"`
	SourceDigest string `json:"source_digest"`
	TargetDigest string `json:"target_digest,omitempty"`
	Entries      int    `json:"entries"`
	AlignmentNS  int64  `json:"alignment_ns"`
	TargetReady  bool   `json:"target_ready"`
}

// Report is the outcome of a run.
type Report struct {
	Stage     string     `json:"stage"`
	Entries   int        `json:"entries"`
	Alignment clock.Time `json:"alignment_ns"`
	Resumed   bool       `json:"resumed"`
	Noop      bool       `json:"noop"`
	Source    string     `json:"source"`
	Target    string     `json:"target"`
	Digest    string     `json:"digest"`
}

// AlignmentMs renders the alignment error in milliseconds.
func (r *Report) AlignmentMs() float64 { return clock.ToMilliseconds(r.Alignment) }

// Rollback restores the target from the pre-migration copy. It is safe to
// call when no rollback copy exists.
func Rollback(dir, targetName string) error {
	target := filepath.Join(dir, targetName)
	backup := target + backupSuffix
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.Rename(backup, target)
}

// LoadPoint reads the rollback point, if one exists. It is exported so the
// CLI can report where a resumable run will restart.
func LoadPoint(dir string) (*Point, error) {
	return loadPoint(filepath.Join(dir, pointName))
}

func loadPoint(path string) (*Point, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Newf(fault.CodeMigrationRollback, "", "cannot read rollback point: %v", err)
	}
	var p Point
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fault.Newf(fault.CodeMigrationRollback, "", "rollback point is corrupt: %v", err)
	}
	if p.Stage == "" {
		return nil, fault.Newf(fault.CodeMigrationRollback, "", "rollback point has no stage")
	}
	return &p, nil
}

func savePoint(path string, p *Point) error {
	if p == nil {
		return nil
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := writeFileAtomic(path, b); err != nil {
		return fault.Newf(fault.CodeMigrationRollback, "", "cannot write rollback point: %v", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".migrate-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	return os.Rename(name, path)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Cleanup removes the rollback artefacts after a successful acceptance.
func Cleanup(dir string) error {
	for _, name := range []string{pointName, tempName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
