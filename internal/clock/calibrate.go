// Package clock provides the shared nanosecond time base used by every stage
// of the pipeline. This file calibrates the mapping onto the reference clock.
package clock

import (
	"math"
	"sort"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// Calibrate estimates per-track rates and establishes the mapping onto the
// reference clock. Tracks with too few observations fall back to the
// reference rate and first-block alignment.
func (tl *Timeline) Calibrate() error {
	ref, ok := tl.tracks[tl.ref]
	if !ok || !ref.haveFirstNs {
		return merr.New("clock.calibrate", merr.CodeBadInput, -1,
			"reference track %d has no observations", tl.ref)
	}
	if slope, ok := fitSlope(ref.samples, tl.opts.MaxDriftPPM); ok && len(ref.samples) >= tl.opts.MinSamples {
		ref.slope = slope
	} else {
		ref.slope = 1
	}
	ref.scale = 1
	ref.baseRawNs = ref.firstRawNs
	ref.baseShare = ref.firstRawNs
	ref.haveShare = true

	for _, tr := range tl.tracks {
		if tr == ref {
			continue
		}
		slope, ok := fitSlope(tr.samples, tl.opts.MaxDriftPPM)
		if !ok || len(tr.samples) < tl.opts.MinSamples {
			slope = ref.slope
		}
		tr.slope = slope
		if !tr.haveFirstNs {
			// Track never produced a block; give it a harmless mapping.
			tr.baseRawNs = 0
			tr.baseShare = ref.firstRawNs
			tr.scale = 1
			tr.haveShare = true
			continue
		}
		// Map target raw time onto the reference clock: divide out the
		// target rate and multiply by the reference rate.
		tr.scale = ref.slope / slope
		if len(tr.anchors) > 0 {
			offsets := make([]float64, len(tr.anchors))
			for i, a := range tr.anchors {
				offsets[i] = float64(a.sharedNs) - float64(a.rawNs-tr.firstRawNs)*tr.scale - float64(tr.firstRawNs)
			}
			sort.Float64s(offsets)
			// Median offset is robust to a single bad marker.
			tr.baseShare = tr.firstRawNs + int64(math.Round(offsets[len(offsets)/2]))
			tr.baseRawNs = tr.firstRawNs
		} else {
			// Preserve the observed start offset on the shared clock; only
			// the rate is corrected. Tracks muxed from one capture clock
			// therefore keep near-zero skew, while a genuinely late device
			// keeps its real offset.
			tr.baseRawNs = tr.firstRawNs
			tr.baseShare = tr.firstRawNs
		}
		tr.haveShare = true
	}
	tl.calibrated = true
	return nil
}

// Corrected maps an unwrapped raw timestamp to the shared clock. Output is
// strictly increasing per track in call order. Calibrate must have been
// called.
func (tl *Timeline) Corrected(t media.TrackID, rawNs int64) (int64, error) {
	if !tl.calibrated {
		return 0, merr.New("clock.corrected", merr.CodeBadInput, -1, "timeline not calibrated")
	}
	tr, ok := tl.tracks[t]
	if !ok {
		return 0, merr.New("clock.corrected", merr.CodeBadInput, -1, "unknown track %d", t)
	}
	if !tr.haveShare {
		return 0, merr.New("clock.corrected", merr.CodeBadInput, -1, "track %d not calibrated", t)
	}
	out := tr.baseShare + int64(math.Round(float64(rawNs-tr.baseRawNs)*tr.scale))
	if tr.haveOutput && out <= tr.lastOut {
		out = tr.lastOut + 1
	}
	tr.lastOut = out
	tr.haveOutput = true
	return out, nil
}
