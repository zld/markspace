package pipeline

import (
	"fmt"
	"os"

	"archive/internal/model"
	"archive/internal/render"
)

// VerifyReport summarises an independent verification pass.
type VerifyReport struct {
	Segments       int
	Frames         int
	KeyframeHits   int
	HashMismatches int
	RoundtripOK    int      // segments whose recorded hash re-derives from the frames
	Problems       []string // human-readable check failures
}

// OK reports whether every check passed.
func (r *VerifyReport) OK() bool {
	return r.HashMismatches == 0 && len(r.Problems) == 0
}

// Verify independently re-reads the output manifest and frame files
// and cross-checks frame counts, timestamp monotonicity, sample
// indices, per-segment hashes and the stage summaries against the
// journal. It never trusts in-memory state from a run.
func Verify(dir string) (*VerifyReport, error) {
	rep := &VerifyReport{}
	m, err := ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	journal, err := LoadJournal(dir)
	if err != nil {
		return nil, err
	}
	// Stage summaries must match the journal checkpoints bound to the
	// same input and policy.
	for _, stage := range StageOrder {
		sum, ok := m.StageSummaries[stage]
		if !ok {
			rep.Problems = append(rep.Problems, fmt.Sprintf("missing stage summary %q", stage))
			continue
		}
		cp, ok := journal.ValidStage(stage, m.InputHash, m.Policy.Version)
		if !ok {
			rep.Problems = append(rep.Problems, fmt.Sprintf("stage %q has no valid journal checkpoint", stage))
			continue
		}
		if cp.Digest != sum.Digest {
			rep.Problems = append(rep.Problems, fmt.Sprintf("stage %q digest mismatch: manifest %s vs journal %s", stage, sum.Digest, cp.Digest))
		}
	}
	rep.Segments = len(m.Segments)
	for i := range m.Segments {
		seg := &m.Segments[i]
		if len(seg.Frames) != seg.SampleCount {
			rep.Problems = append(rep.Problems, fmt.Sprintf("segment %d: %d frames for %d samples", seg.Index, len(seg.Frames), seg.SampleCount))
		}
		var paths []string
		prevSample := -1
		for j, fr := range seg.Frames {
			if fr.Index != j {
				rep.Problems = append(rep.Problems, fmt.Sprintf("segment %d frame %d: index %d out of sequence", seg.Index, j, fr.Index))
			}
			if int(fr.SampleIndex) >= seg.SampleCount || int(fr.SampleIndex) <= prevSample {
				rep.Problems = append(rep.Problems, fmt.Sprintf("segment %d frame %d: bad sample index %d", seg.Index, j, fr.SampleIndex))
			}
			prevSample = int(fr.SampleIndex)
			if j > 0 && seg.Frames[j-1].Start.Cmp(fr.Start) >= 0 {
				rep.Problems = append(rep.Problems, fmt.Sprintf("segment %d frame %d: non-monotonic timestamp", seg.Index, j))
			}
			p, err := CheckRelPath(dir, fr.File)
			if err != nil {
				rep.Problems = append(rep.Problems, fmt.Sprintf("segment %d frame %d: %v", seg.Index, j, err))
				continue
			}
			if _, err := os.Stat(p); err != nil {
				rep.Problems = append(rep.Problems, fmt.Sprintf("segment %d frame %d: missing %s", seg.Index, j, fr.File))
				continue
			}
			paths = append(paths, p)
			if fr.Keyframe {
				rep.KeyframeHits++
			}
			rep.Frames++
		}
		if len(paths) == len(seg.Frames) {
			digest, err := render.SegmentDigest(seg.Index, paths)
			if err != nil {
				return nil, err
			}
			if digest != seg.SHA256 {
				rep.HashMismatches++
			} else {
				rep.RoundtripOK++
			}
		}
	}
	return rep, nil
}

// FaultForProblems converts verification problems into a verify-stage
// fault for exit-code handling.
func FaultForProblems(rep *VerifyReport) error {
	if rep.OK() {
		return nil
	}
	msg := fmt.Sprintf("%d problem(s), %d hash mismatch(es)", len(rep.Problems), rep.HashMismatches)
	return model.StageFault(model.StageVerify, -1, -1, nil, "%s", msg)
}
