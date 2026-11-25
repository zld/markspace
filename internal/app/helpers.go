package app

import (
	"os"
	"sort"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/manifest"
)

// alignment is stored between stages on the Pipeline.
// (declared here to keep the stage files focused.)

// listFragmentFiles returns fragment base names in deterministic numeric
// (input-index) order for one representation directory.
func listFragmentFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if fixture.IsFragmentName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names, nil
}

// resumeCheckpoint loads the prior checkpoint when resuming; nil otherwise.
func (p *Pipeline) resumeCheckpoint() *checkpoint.Checkpoint {
	return p.existingCheckpoint()
}

// existingCheckpoint returns the persisted checkpoint or nil when absent.
func (p *Pipeline) existingCheckpoint() *checkpoint.Checkpoint {
	if !p.store.Exists() {
		return nil
	}
	cp, err := p.store.Load()
	if err != nil {
		return nil
	}
	return cp
}

// buildCheckpoint carries input digests forward and sets the target stage.
func (p *Pipeline) buildCheckpoint(prev *checkpoint.Checkpoint, stage checkpoint.Stage) *checkpoint.Checkpoint {
	cp := &checkpoint.Checkpoint{
		Stage:        stage,
		InputDigests: map[string]string{},
		Case:         string(p.cfg.Case),
	}
	if prev != nil {
		cp.ManifestVersion = prev.ManifestVersion
		cp.ManifestSHA256 = prev.ManifestSHA256
		cp.CompletedFragments = append(cp.CompletedFragments, prev.CompletedFragments...)
		for k, v := range prev.InputDigests {
			cp.InputDigests[k] = v
		}
	}
	for _, st := range p.states {
		for _, f := range st.frags {
			cp.InputDigests[f.InputPath] = f.SHA256
		}
	}
	return cp
}

// classify enriches the summary from a fault error and returns it unchanged
// so the adapter can print a stable code and pick a non-zero exit code.
func (p *Pipeline) classify(err error, summary *Summary) error {
	if fe, ok := err.(*fault.Error); ok {
		summary.ErrorCode = string(fe.Code)
		summary.ErrorInput = fe.Input
		summary.RecoveryHint = fe.Hint
		if fe.Recoverable {
			summary.SafeResumeStage = summary.CheckpointStage
		}
		return fe
	}
	summary.ErrorCode = "internal"
	return err
}

// VerifyResult is the terminal output of the standalone verify command.
type VerifyResult struct {
	SegmentCount        int    `json:"segment_count"`
	RepresentationCount int    `json:"representation_count"`
	GapCount            int    `json:"gap_count"`
	RegressionCount     int    `json:"regression_count"`
	ManifestSHA256      string `json:"manifest_sha256"`
	Valid               bool   `json:"valid"`
}

// Verify loads a manifest file, re-validates it and hashes its canonical
// form. A validation failure returns a classified error alongside the result
// so the adapter can exit non-zero while still printing the fields.
func Verify(manifestPath string) (*VerifyResult, error) {
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return nil, fault.New(fault.CodeManifest, "cannot load manifest: %v", err).WithInput(manifestPath)
	}
	digest, err := m.SHA256()
	if err != nil {
		return nil, err
	}
	res := &VerifyResult{
		SegmentCount:        m.SegmentCount,
		RepresentationCount: m.RepresentationCount,
		GapCount:            m.GapCount,
		RegressionCount:     m.RegressionCount,
		ManifestSHA256:      digest,
	}
	if err := m.Validate(); err != nil {
		return res, err
	}
	res.Valid = true
	return res, nil
}
