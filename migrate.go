package mediakernel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ErrInterrupted marks a deliberately interrupted migration. The original
// manifest is always preserved when this is returned.
var ErrInterrupted = NewError(PhaseMigrate, -1, "migration interrupted")

// InterruptHook lets callers simulate a crash after a named stage. Returning
// an error aborts the migration at that boundary.
type InterruptHook func(stage string) error

// Migrator runs the three-stage manifest migration (validate -> stage ->
// commit). All file work happens inside one directory: the new manifest is
// written to a same-directory temp file and installed with an atomic rename,
// while the previous document is kept as a rollback point until commit lands.
type Migrator struct {
	dir          string
	manifestName string
	hook         InterruptHook
}

// NewMigrator creates a migrator for <dir>/<manifestName>.
func NewMigrator(dir, manifestName string) *Migrator {
	return &Migrator{dir: dir, manifestName: manifestName}
}

// SetInterruptHook installs (or clears with nil) a stage-boundary hook.
func (m *Migrator) SetInterruptHook(h InterruptHook) { m.hook = h }

// ManifestPath is the final manifest location.
func (m *Migrator) ManifestPath() string { return filepath.Join(m.dir, m.manifestName) }

// RollbackPath is the pre-commit rollback point.
func (m *Migrator) RollbackPath() string { return m.ManifestPath() + ".rollback" }

func (m *Migrator) tempPath() string {
	return filepath.Join(m.dir, "."+m.manifestName+".tmp")
}

func (m *Migrator) fire(stage string) error {
	if m.hook == nil {
		return nil
	}
	if err := m.hook(stage); err != nil {
		return err
	}
	return nil
}

// MigrationReport summarizes one migration attempt.
type MigrationReport struct {
	Stage             string  `json:"stage"`
	Committed         bool    `json:"committed"`
	RollbackAvailable bool    `json:"rollback_available"`
	ManifestDigest    *Digest `json:"manifest_digest,omitempty"`
}

// Run executes the migration. next must already carry its entries; the
// migrator fills Stage and ManifestDigest. On any failure the manifest at
// ManifestPath is left byte-identical to before the call.
func (m *Migrator) Run(next *Manifest) (*MigrationReport, error) {
	// Stage 1: validate both the incoming document and any existing manifest.
	if err := next.Validate(); err != nil {
		return &MigrationReport{Stage: StageValidate}, err
	}
	if existing, err := os.ReadFile(m.ManifestPath()); err == nil {
		if _, derr := DecodeManifest(existing); derr != nil {
			return &MigrationReport{Stage: StageValidate}, derr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &MigrationReport{Stage: StageValidate},
			NewError(PhaseMigrate, -1, "cannot read existing manifest").WithCause(err)
	}
	if err := m.fire(StageValidate); err != nil {
		return &MigrationReport{Stage: StageValidate}, ErrInterrupted.WithCause(err)
	}

	// Stage 2: stage the new document in a same-directory temp file and keep
	// the current document as a rollback point. The target path is untouched.
	if err := m.createRollbackPoint(); err != nil {
		return &MigrationReport{Stage: StageStage}, err
	}
	next.Stage = StageStage
	if err := m.writeTemp(next); err != nil {
		_ = os.Remove(m.tempPath())
		return &MigrationReport{Stage: StageStage, RollbackAvailable: m.HasRollback()}, err
	}
	if err := m.fire(StageStage); err != nil {
		_ = os.Remove(m.tempPath()) // staged file discarded; original stays in place
		return &MigrationReport{Stage: StageStage, RollbackAvailable: m.HasRollback()},
			ErrInterrupted.WithCause(err)
	}

	// Stage 3: finalize and atomically replace the target.
	next.Stage = StageDone
	if err := next.ComputeDigest(); err != nil {
		_ = os.Remove(m.tempPath())
		return &MigrationReport{Stage: StageCommit, RollbackAvailable: m.HasRollback()}, err
	}
	if err := m.writeTemp(next); err != nil {
		_ = os.Remove(m.tempPath())
		return &MigrationReport{Stage: StageCommit, RollbackAvailable: m.HasRollback()}, err
	}
	if err := m.fire(StageCommit); err != nil {
		_ = os.Remove(m.tempPath()) // rename never happened: original is intact
		return &MigrationReport{Stage: StageCommit, RollbackAvailable: m.HasRollback()},
			ErrInterrupted.WithCause(err)
	}
	if err := os.Rename(m.tempPath(), m.ManifestPath()); err != nil {
		return &MigrationReport{Stage: StageCommit, RollbackAvailable: m.HasRollback()},
			NewError(PhaseMigrate, -1, "atomic rename failed").WithCause(err)
	}
	syncDir(m.dir)
	_ = os.Remove(m.RollbackPath()) // commit landed; rollback point no longer needed
	d := next.ManifestDigest
	return &MigrationReport{Stage: StageDone, Committed: true, ManifestDigest: &d}, nil
}

// createRollbackPoint copies the current manifest bytes aside. When no
// manifest exists yet, an empty rollback marker records that fact so Restore
// knows to delete rather than replace.
func (m *Migrator) createRollbackPoint() error {
	raw, err := os.ReadFile(m.ManifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(m.RollbackPath(), nil, 0o644)
	}
	if err != nil {
		return NewError(PhaseMigrate, -1, "rollback copy failed").WithCause(err)
	}
	return os.WriteFile(m.RollbackPath(), raw, 0o644)
}

// HasRollback reports whether a pre-commit rollback point exists.
func (m *Migrator) HasRollback() bool {
	_, err := os.Stat(m.RollbackPath())
	return err == nil
}

// Restore reinstates the rollback point: an empty marker means there was no
// prior manifest, so the current target is removed; otherwise the saved bytes
// atomically replace the target.
func (m *Migrator) Restore() error {
	raw, err := os.ReadFile(m.RollbackPath())
	if err != nil {
		return NewError(PhaseMigrate, -1, "no rollback point to restore").WithCause(err)
	}
	if len(raw) == 0 {
		if err := os.Remove(m.ManifestPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return NewError(PhaseMigrate, -1, "restore remove failed").WithCause(err)
		}
	} else if err := os.WriteFile(m.tempPath(), raw, 0o644); err != nil {
		return NewError(PhaseMigrate, -1, "restore temp write failed").WithCause(err)
	} else if err := os.Rename(m.tempPath(), m.ManifestPath()); err != nil {
		return NewError(PhaseMigrate, -1, "restore rename failed").WithCause(err)
	}
	syncDir(m.dir)
	return os.Remove(m.RollbackPath())
}

func (m *Migrator) writeTemp(man *Manifest) error {
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return NewError(PhaseMigrate, -1, "manifest marshal failed").WithCause(err)
	}
	if err := os.WriteFile(m.tempPath(), raw, 0o644); err != nil {
		return NewError(PhaseMigrate, -1, "temp manifest write failed").WithCause(err)
	}
	return syncPath(m.tempPath())
}

func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
