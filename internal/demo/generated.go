package demo

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/fixture"
	"mediacore/internal/index"
	"mediacore/internal/pipeline"
)

// PrepareGenerated creates the normal fixture and returns its manifest path.
func PrepareGenerated(workdir string) (string, error) {
	fixtureDir := filepath.Join(workdir, "fixture")
	set, err := fixture.GenerateNormal(fixtureDir)
	if err != nil {
		return "", err
	}
	return set.ManifestPath, nil
}

// RunGenerated executes one pipeline attempt. ErrSimulatedCrash may be returned
// with a non-nil report after the publish product is durable.
func RunGenerated(workdir, manifestPath string, r index.Range, crashAfter string) (*pipeline.Report, error) {
	rn := pipeline.NewRunner(workdir, manifestPath, "acceptance-batch", r)
	rn.CrashAfter = crashAfter
	return rn.Run()
}

// WritePreCrashReport persists the first attempt's hashes for parent comparison.
func WritePreCrashReport(workdir string, rep *pipeline.Report) error {
	if rep == nil {
		return nil
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workdir, "pre-crash-report.json"), data, 0o644)
}

// ReadPreCrashReport reads the child attempt report.
func ReadPreCrashReport(workdir string) (*pipeline.Report, error) {
	data, err := os.ReadFile(filepath.Join(workdir, "pre-crash-report.json"))
	if err != nil {
		return nil, err
	}
	var rep pipeline.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// SameHashes reports whether two runs produced identical segment hashes.
func SameHashes(a, b *pipeline.Report) bool {
	if a == nil || b == nil || len(a.SegmentHashes) != len(b.SegmentHashes) {
		return false
	}
	for i := range a.SegmentHashes {
		if a.SegmentHashes[i] != b.SegmentHashes[i] {
			return false
		}
	}
	return true
}
