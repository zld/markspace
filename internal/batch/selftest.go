package batch

import (
	"errors"
	"path/filepath"
	"strconv"

	"replaycheck/internal/fixture"
	"replaycheck/internal/index"
)

// SelfTestReport contains all three deterministic runs used by the demo.
type SelfTestReport struct {
	Set            *fixture.Set
	Interrupted    *Result
	Recovery       *Result
	FreshRepeat    *Result
	SecondResume   *Result
	Checks         []Check
	RecoveryDigest string
	RepeatDigest   string
	SecondDigest   string
	DigestMatch    bool
	RecomputedOK   bool
}

// Check is one acceptance assertion evaluated by the self-test.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// RunSelfTest generates the five bundled samples, interrupts at the corrupt
// fifth block, resumes, and proves repeated indexes have the same digest.
func RunSelfTest(indexPath string, chunkEvery int) (*SelfTestReport, error) {
	if indexPath == "" {
		indexPath = filepath.Join("tmp", "index.json")
	}
	if chunkEvery <= 0 {
		chunkEvery = 4
	}
	dir := filepath.Dir(indexPath)
	sampleDir := filepath.Join(dir, "samples")
	set, err := fixture.GenerateSet(sampleDir)
	if err != nil {
		return nil, err
	}

	checkpointPath := indexPath + ".checkpoint.json"
	repeatPath := filepath.Join(dir, "index-repeat.json")
	repeatCheckpoint := repeatPath + ".checkpoint.json"

	interrupted, err := Run(Config{
		Samples: set.Samples, CheckpointPath: checkpointPath,
		ChunkEvery: chunkEvery, InterruptKey: "chunk_boundary_corruption",
	})
	if !errors.Is(err, ErrInterrupted) {
		return nil, errors.New("self-test expected interruption at corrupt chunk block")
	}

	recovery, err := Run(Config{
		Samples: set.Samples, IndexPath: indexPath, CheckpointPath: checkpointPath,
		ChunkEvery: chunkEvery, Resume: true,
	})
	if err != nil {
		return nil, err
	}

	freshRepeat, err := Run(Config{
		Samples: set.Samples, IndexPath: repeatPath, CheckpointPath: repeatCheckpoint,
		ChunkEvery: chunkEvery,
	})
	if err != nil {
		return nil, err
	}

	secondResume, err := Run(Config{
		Samples: set.Samples, IndexPath: indexPath, CheckpointPath: checkpointPath,
		ChunkEvery: chunkEvery, Resume: true,
	})
	if err != nil {
		return nil, err
	}

	r := &SelfTestReport{
		Set: set, Interrupted: interrupted, Recovery: recovery,
		FreshRepeat: freshRepeat, SecondResume: secondResume,
		RecoveryDigest: recovery.IndexSHA256, RepeatDigest: freshRepeat.IndexSHA256,
		SecondDigest: secondResume.IndexSHA256,
	}
	r.DigestMatch = r.RecoveryDigest == r.RepeatDigest && r.RecoveryDigest == r.SecondDigest
	r.RecomputedOK = len(recovery.RecomputedBlocks) == 1 &&
		recovery.RecomputedBlocks[0] == "chunk_boundary_corruption" &&
		len(secondResume.RecomputedBlocks) == 0
	r.Checks = evaluateSelfTest(r)
	return r, nil
}

// OK reports whether every self-test assertion passed.
func (r *SelfTestReport) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return r.DigestMatch && r.RecomputedOK
}

func evaluateSelfTest(r *SelfTestReport) []Check {
	m := r.Recovery.Metrics
	checks := []Check{
		{Name: "files=5", OK: m.Files == 5, Detail: strconv.Itoa(m.Files)},
		{Name: "tracks_checked=3", OK: m.TracksChecked == 3, Detail: strconv.Itoa(m.TracksChecked)},
		{Name: "wrap_count=1", OK: m.WrapCount == 1, Detail: strconv.Itoa(m.WrapCount)},
		{Name: "drift<=2ms", OK: m.MaxDriftMS <= 2.0, Detail: strconv.FormatFloat(m.MaxDriftMS, 'f', 3, 64)},
		{Name: "vfr_boundary<=one_frame(50ms)", OK: m.VFRBoundaryErrorMS >= 0 && m.VFRBoundaryErrorMS <= 50.0, Detail: strconv.FormatFloat(m.VFRBoundaryErrorMS, 'f', 3, 64)},
		{Name: "anomaly_rate<=1%", OK: m.AnomalyRatePct <= 1.0, Detail: strconv.FormatFloat(m.AnomalyRatePct, 'f', 4, 64)},
		{Name: "budget_deviation<=2%", OK: m.BudgetDeviationPct <= 2.0, Detail: strconv.FormatFloat(m.BudgetDeviationPct, 'f', 4, 64)},
		{Name: "recovery_recomputed_one_unconfirmed_block", OK: r.RecomputedOK},
		{Name: "repeated_index_digest_matches", OK: r.DigestMatch},
	}

	for _, name := range []string{"truncated_header", "chunk_boundary_corruption"} {
		offset, found := failedOffset(r.Recovery.Index.Conclusions, name)
		checks = append(checks, Check{
			Name:   name + "_located_failure",
			OK:     found && offset >= 0,
			Detail: "byte_offset=" + strconv.FormatInt(offset, 10),
		})
	}
	return checks
}

func failedOffset(conclusions []index.FileConclusion, name string) (int64, bool) {
	for _, c := range conclusions {
		if !containsSubstring(c.File, name) {
			continue
		}
		for _, a := range c.Anomalies {
			if a.Offset >= 0 {
				return a.Offset, true
			}
		}
	}
	return -1, false
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
