package pipeline

import (
	"path/filepath"

	"medkernel/txn"
)

// CrashReport describes the outcome of an interrupt-and-recover simulation.
type CrashReport struct {
	FullHash    string
	RecoverHash string
	Rollbacks   int
	Recovered   bool // hashes match and reuse actually occurred
}

// SimulateCrashRecovery commits only the ts_parse boundary into dir (as if
// the process were killed immediately afterwards), then reopens and completes
// every remaining stage over the same inputs. It compares the recovered
// commit hash against an independent full rerun in refDir.
func SimulateCrashRecovery(dir, refDir string, seed int64, thresholds Thresholds) (CrashReport, error) {
	g := NewGenerator(seed)
	tsfx := g.BuildTS(ScenNormal, 24)
	rtpfx := g.BuildRTP(ScenNormal, 60)

	// Independent uninterrupted reference.
	ref := NewRunner(refDir, thresholds)
	refM, err := ref.Run(ScenNormal, tsfx, rtpfx)
	if err != nil {
		return CrashReport{}, err
	}

	// Write a single committed boundary, then stop (simulated crash).
	commit, err := txn.NewCommitter(dir)
	if err != nil {
		return CrashReport{}, err
	}
	staged := NewRunner(dir, thresholds)
	if _, _, err := commit.Execute(txn.Stage{
		Name:      "ts_parse",
		InputHash: hashBuffer(tsfx),
		Run: func(workdir string) (string, map[string]float64, error) {
			return staged.stageParse(workdir, tsfx)
		},
	}); err != nil {
		return CrashReport{}, err
	}
	if err := commit.Close(); err != nil {
		return CrashReport{}, err
	}

	// Recovery run over identical inputs.
	rec := NewRunner(filepath.Join(dir), thresholds)
	recM, err := rec.Run(ScenNormal, tsfx, rtpfx)
	if err != nil {
		return CrashReport{}, err
	}
	return CrashReport{
		FullHash:    refM.CommitHash,
		RecoverHash: recM.CommitHash,
		Rollbacks:   recM.RollbackCount,
		Recovered:   refM.CommitHash == recM.CommitHash,
	}, nil
}
