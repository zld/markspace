package pipeline

import (
	"fmt"

	"mediacheck/internal/align"
	"mediacheck/internal/ebml"
	"mediacheck/internal/manifest"
)

// CheckInvariants verifies the cross-stage guarantees before a manifest is
// committed. Any violation fails closed (exit category 2): segment continuity,
// representation boundary consistency, and manifest digest completeness.
func CheckInvariants(
	mf *manifest.Manifest,
	boundaries []align.Boundary,
	reps []parsedRep,
	checkpointEvery int,
	dryRun bool,
) *Error {
	if err := checkSegmentContinuity(mf); err != nil {
		return materialError(ebml.ErrBadElement, -1, "%v", err)
	}
	if err := checkBoundaryConsistency(mf, boundaries, reps); err != nil {
		return materialError(ebml.ErrBadElement, -1, "%v", err)
	}
	if err := checkManifestDigest(mf); err != nil {
		return materialError(ebml.ErrBadElement, -1, "%v", err)
	}
	if err := checkCursorPolicy(mf, checkpointEvery, dryRun); err != nil {
		return indexError("%v", err)
	}
	return nil
}

// checkSegmentContinuity: indices are gap-free, T0 strictly increasing and
// every duration is positive.
func checkSegmentContinuity(mf *manifest.Manifest) error {
	if len(mf.Segments) < 2 {
		return fmt.Errorf("invariant: fewer than two segments")
	}
	for i, s := range mf.Segments {
		if s.Index != i {
			return fmt.Errorf("invariant: segment index gap at %d (got %d)", i, s.Index)
		}
		if s.DurationNS <= 0 {
			return fmt.Errorf("invariant: non-positive duration for segment %d", i)
		}
		if i > 0 && s.T0NS <= mf.Segments[i-1].T0NS {
			return fmt.Errorf("invariant: segment T0 not strictly increasing at %d", i)
		}
	}
	return nil
}

// checkBoundaryConsistency: every manifest segment references a valid frame
// index in every representation and all layers share the same boundary T0.
func checkBoundaryConsistency(mf *manifest.Manifest, boundaries []align.Boundary, reps []parsedRep) error {
	repLen := map[string]int{}
	for _, r := range reps {
		repLen[r.ID] = len(r.Frames)
	}
	for i, s := range mf.Segments {
		if len(s.RepFrameIdx) != len(reps) {
			return fmt.Errorf("invariant: segment %d references %d layers, want %d",
				i, len(s.RepFrameIdx), len(reps))
		}
		for id, idx := range s.RepFrameIdx {
			n, ok := repLen[id]
			if !ok {
				return fmt.Errorf("invariant: segment %d references unknown rep %s", i, id)
			}
			if idx < 0 || idx >= n {
				return fmt.Errorf("invariant: segment %d rep %s frame index %d out of range [0,%d)",
					i, id, idx, n)
			}
		}
		if s.T0NS != boundaries[i].T0 {
			return fmt.Errorf("invariant: manifest T0 %d != aligned boundary %d at segment %d",
				s.T0NS, boundaries[i].T0, i)
		}
	}
	// Representation summaries must all report the same segment count.
	want := len(mf.Segments)
	for _, r := range mf.Representations {
		if r.Segments != want {
			return fmt.Errorf("invariant: rep %s reports %d segments, want %d", r.ID, r.Segments, want)
		}
	}
	return nil
}

// checkManifestDigest: all digests are present and the output hash is exactly
// 64 hex characters (a SHA-256).
func checkManifestDigest(mf *manifest.Manifest) error {
	if len(mf.InputSHA256) != 64 || len(mf.ConfigSHA256) != 64 || len(mf.OutputSHA256) != 64 {
		return fmt.Errorf("invariant: manifest digests incomplete (in=%d cfg=%d out=%d)",
			len(mf.InputSHA256), len(mf.ConfigSHA256), len(mf.OutputSHA256))
	}
	for name, v := range map[string]string{
		"input": mf.InputSHA256, "config": mf.ConfigSHA256, "output": mf.OutputSHA256,
	} {
		if !isHex64(v) {
			return fmt.Errorf("invariant: %s digest is not 64 hex chars", name)
		}
	}
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// checkCursorPolicy documents the checkpoint cadence. In a dry run no index is
// written, so cursor invariants are vacuously satisfied.
func checkCursorPolicy(mf *manifest.Manifest, checkpointEvery int, dryRun bool) error {
	if dryRun {
		return nil
	}
	if checkpointEvery < 1 {
		return fmt.Errorf("invariant: checkpoint cadence must be >=1")
	}
	return nil
}
