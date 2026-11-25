package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/fixture"
	"mediasync/internal/report"
	"mediasync/internal/txn"
)

// inputFile is the persisted full fixture kept under the run directory so
// verify/recover never depend on regenerated randomness.
const inputFile = "input.fixture.json"

// PersistFixture writes the full (uncut) fixture to the run directory. It is
// written before any stage runs and is outside the staged transaction space,
// so a crashed stage never leaves it half-committed.
func PersistFixture(outDir string, fx *fixture.Fixture) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(outDir, inputFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(outDir, inputFile))
}

// LoadFixture reads a previously persisted fixture.
func LoadFixture(outDir string) (*fixture.Fixture, error) {
	data, err := os.ReadFile(filepath.Join(outDir, inputFile))
	if err != nil {
		return nil, fmt.Errorf("pipeline: load fixture: %w", err)
	}
	var fx fixture.Fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		return nil, err
	}
	return &fx, nil
}

// Recover reconciles a possibly interrupted run directory: committed stages
// are verified, and any uncommitted staging is rolled back. It returns the
// ordered list of durably committed stage names.
func Recover(outDir string) ([]string, error) {
	mgr, err := txn.NewManager(outDir)
	if err != nil {
		return nil, err
	}
	defer mgr.Close()
	return mgr.Recover()
}

// Verify recomputes the entire pipeline from the persisted fixture and
// compares the result against the committed summary and manifests, without
// mutating the run directory. It enforces that every alignment error is
// within toleranceMs and all artifact hashes match.
func Verify(outDir string, toleranceMs int64) error {
	fx, err := LoadFixture(outDir)
	if err != nil {
		return err
	}
	cfg := DefaultConfig(fx.Seed)
	// Run into a throwaway directory so verification writes nothing durable.
	tmp, err := os.MkdirTemp("", "mediasync-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	p, err := New(cfg, tmp)
	if err != nil {
		return err
	}
	defer p.Close()
	recomputed, err := p.Run(fx)
	if err != nil {
		return fmt.Errorf("pipeline: recompute failed: %w", err)
	}

	// 1. Compare against the persisted summary, item by item.
	storedBytes, err := os.ReadFile(filepath.Join(outDir, "committed", txn.StageReport, "summary.json"))
	if err != nil {
		return err
	}
	var stored report.Summary
	if err := json.Unmarshal(storedBytes, &stored); err != nil {
		return err
	}
	if err := report.Compare(&stored, recomputed.Summary); err != nil {
		return err
	}

	// 2. Re-verify every committed manifest hash on disk.
	mgr, err := txn.NewManager(outDir)
	if err != nil {
		return err
	}
	defer mgr.Close()
	for _, st := range txn.Stages {
		if _, err := os.Stat(filepath.Join(mgr.CommittedDir(st), "manifest.json")); err == nil {
			if err := mgr.VerifyManifest(st); err != nil {
				return err
			}
		}
	}

	// 3. Alignment error tolerance gate (default requirement: <= 1 ms).
	if err := checkAlignmentTolerance(recomputed.Summary, toleranceMs); err != nil {
		return err
	}
	return nil
}

func checkAlignmentTolerance(s *report.Summary, toleranceMs int64) error {
	// Rendered artifacts are produced exactly on their cues (align error 0),
	// and the summary records the measured A/V drift; gate both.
	if s.Alignment.MaxDriftMs > toleranceMs {
		return fmt.Errorf("pipeline: max drift %d ms exceeds tolerance %d ms",
			s.Alignment.MaxDriftMs, toleranceMs)
	}
	for _, f := range s.Frames {
		if f.AlignError != 0 {
			return fmt.Errorf("pipeline: frame %d align error %d ticks", f.Index, f.AlignError)
		}
	}
	for _, a := range s.Audio {
		if a.AlignError != 0 {
			return fmt.Errorf("pipeline: audio %d align error %d ticks", a.Index, a.AlignError)
		}
	}
	return nil
}
