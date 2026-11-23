package manifest

import (
	"fmt"

	"mediad/timeline"
)

// VerificationError describes a failed cross-stage invariant with context.
type VerificationError struct {
	Op     string
	Detail string
}

func (e *VerificationError) Error() string {
	return fmt.Sprintf("manifest verify: %s: %s", e.Op, e.Detail)
}

// Inputs bundles the three stages that must agree on one time base.
type Inputs struct {
	Timeline   *timeline.Timeline
	Manifest   Manifest
	FrameCount int
}

// Verify enforces:
//   - manifest hash matches a re-hash of its content (tamper/misalignment),
//   - frame times vs scheduled times differ by at most 1 nanosecond,
//   - track ordering and event counts are conserved,
//   - frame count is conserved between producer and manifest,
//   - no duplicate frame indices/times,
//   - manifest is no longer in the incomplete state after export.
func Verify(in Inputs) error {
	m := in.Manifest
	if m.State == StateIncomplete {
		return &VerificationError{"state", "manifest still incomplete after export"}
	}
	want := hashManifest(m)
	if want != m.SHA256 {
		return &VerificationError{"hash", fmt.Sprintf("manifest sha256 mismatch: stored=%s recomputed=%s", m.SHA256, want)}
	}
	if len(m.Frames) != in.FrameCount {
		return &VerificationError{"conservation", fmt.Sprintf("frame count: manifest=%d producer=%d", len(m.Frames), in.FrameCount)}
	}
	seenIdx := map[int64]bool{}
	seenTime := map[int64]bool{}
	for i, fr := range m.Frames {
		if seenIdx[fr.Index] {
			return &VerificationError{"duplicate", fmt.Sprintf("duplicate frame index %d", fr.Index)}
		}
		seenIdx[fr.Index] = true
		if seenTime[fr.TimeNS] {
			return &VerificationError{"duplicate", fmt.Sprintf("duplicate frame time %d", fr.TimeNS)}
		}
		seenTime[fr.TimeNS] = true
		scheduled := int64(i) * 1_000_000_000 / int64(m.FPS)
		if d := abs64(fr.TimeNS - scheduled); d > 1 {
			return &VerificationError{"timebase", fmt.Sprintf("frame %d time %d off scheduled %d by %dns (>1ns)",
				fr.Index, fr.TimeNS, scheduled, d)}
		}
	}
	if m.MaxErrorNS > 1 {
		return &VerificationError{"timebase", fmt.Sprintf("manifest max error %dns exceeds 1ns", m.MaxErrorNS)}
	}
	if in.Timeline != nil {
		if len(m.TrackIDs) != len(in.Timeline.Tracks) {
			return &VerificationError{"conservation", fmt.Sprintf("track count: manifest=%d timeline=%d",
				len(m.TrackIDs), len(in.Timeline.Tracks))}
		}
		for i, tr := range in.Timeline.Tracks {
			if m.TrackIDs[i] != tr.ID {
				return &VerificationError{"order", fmt.Sprintf("track order diverges at %d: manifest=%d timeline=%d",
					i, m.TrackIDs[i], tr.ID)}
			}
		}
		if m.EventCount != in.Timeline.Stats.EventCount {
			return &VerificationError{"conservation", fmt.Sprintf("event count: manifest=%d timeline=%d",
				m.EventCount, in.Timeline.Stats.EventCount)}
		}
	}
	return nil
}

// VerifyHash exposes the stable digest computation for recovery comparisons.
func VerifyHash(m Manifest) string { return hashManifest(m) }
