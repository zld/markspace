package pipeline

import (
	"mediapack/internal/manifest"
	"mediapack/internal/model"
)

// VerifyResult is the CLI-facing outcome of one or more verify passes.
type VerifyResult struct {
	OK                bool
	ManifestHash      string
	FrameCount        int
	LayerCount        int
	FrameChecks       int
	SegmentChecks     int
	Differences       []string
	RepeatDifferences int
	Passes            int
}

// Verify re-reads the sealed artifacts and, when repeat > 1, reruns the same
// checks that many times. RepeatDifferences counts disagreements between the
// first pass and the additional passes; an idempotent verify always reports 0.
func Verify(dir string, repeat int) (*VerifyResult, error) {
	if repeat < 1 {
		repeat = 1
	}
	first, err := manifest.Verify(dir)
	if err != nil {
		return &VerifyResult{OK: false, Differences: []string{err.Error()}}, err
	}
	out := &VerifyResult{
		OK: first.OK, ManifestHash: first.ManifestHash,
		FrameCount: first.FrameCount, LayerCount: first.LayerCount,
		FrameChecks: first.FrameChecks, SegmentChecks: first.SegmentChecks,
		Differences: append([]string(nil), first.Differences...),
		Passes:      repeat,
	}
	for pass := 1; pass < repeat; pass++ {
		next, err := manifest.Verify(dir)
		if err != nil {
			return nil, err
		}
		if next.OK != first.OK ||
			next.FrameChecks != first.FrameChecks ||
			next.SegmentChecks != first.SegmentChecks ||
			len(next.Differences) != len(first.Differences) {
			out.RepeatDifferences++
			continue
		}
		for i := range first.Differences {
			if next.Differences[i] != first.Differences[i] {
				out.RepeatDifferences++
				break
			}
		}
	}
	if out.RepeatDifferences > 0 {
		out.OK = false
	}
	return out, nil
}

// IsChecksumFailure lets the CLI distinguish verify failures (exit 3) from
// usage/state failures.
func IsChecksumFailure(err error) bool {
	return model.IsKind(err, model.KindChecksum)
}
