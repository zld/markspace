package manifest

import (
	"errors"
	"fmt"

	"mediad/internal/clock"
)

// Acceptance thresholds from the media-kernel requirements.
const (
	// MaxBitrateErrPct bounds the absolute target-vs-actual bitrate error.
	MaxBitrateErrPct = 8.0
	// MinQualityMean bounds the mean representation quality.
	MinQualityMean = 0.90
	// MaxAlignmentErrorMS bounds the worst cross-layer boundary error.
	MaxAlignmentErrorMS int64 = 20
)

// Verify checks the cross-module invariants of a built manifest: monotonic
// contiguous segments, per-layer bitrate error, mean quality, alignment
// error and that every still-available representation is listed. Violations
// are returned joined; a recoverable manifest error means the manifest stays
// usable for the healthy layers.
func Verify(m *Manifest, expectedReps int) error {
	if m == nil {
		return clock.NewError(clock.StageManifest, "nil manifest", false)
	}
	var errs error
	add := func(format string, args ...interface{}) {
		errs = errors.Join(errs, clock.NewError(clock.StageManifest,
			fmt.Sprintf(format, args...), true))
	}

	if len(m.Representations) != expectedReps {
		add("representations=%d, want %d", len(m.Representations), expectedReps)
	}
	// Segment boundaries must be contiguous and monotonic on the shared clock.
	for i, seg := range m.Segments {
		wantStart := int64(i) * m.SegmentTicks
		if seg.StartTick != wantStart || seg.EndTick <= seg.StartTick {
			add("segment %d boundary not monotonic: [%d,%d)",
				i, seg.StartTick, seg.EndTick)
		}
		if len(seg.Present) != expectedReps {
			add("segment %d present-vector length %d, want %d",
				i, len(seg.Present), expectedReps)
		}
	}

	// Thresholds apply to healthy representations only. A layer that was
	// damaged and recovered is still listed (failure isolation succeeded),
	// but its degraded bitrate/quality are expected fault symptoms rather
	// than invariant violations.
	var qualitySum float64
	healthy := 0
	for _, r := range m.Representations {
		if r.Quality < 0 || r.Quality > 1 {
			add("rep %d quality %.4f outside [0,1]", r.Index, r.Quality)
		}
		if r.Isolated {
			continue
		}
		healthy++
		qualitySum += r.Quality
		if r.BitrateErrPct > MaxBitrateErrPct {
			add("rep %d bitrate error %.2f%% exceeds %.0f%%",
				r.Index, r.BitrateErrPct, MaxBitrateErrPct)
		}
	}
	if healthy > 0 {
		mean := qualitySum / float64(healthy)
		if mean < MinQualityMean {
			add("quality_mean %.4f below %.2f over %d healthy layers",
				mean, MinQualityMean, healthy)
		}
	}
	if m.AlignmentErrorMS > MaxAlignmentErrorMS {
		add("alignment error %dms exceeds %dms", m.AlignmentErrorMS, MaxAlignmentErrorMS)
	}
	return errs
}

// QualityMean returns the mean quality over healthy (non-isolated)
// representations; it reports 0 when no healthy representation exists.
func QualityMean(m *Manifest) float64 {
	if m == nil || len(m.Representations) == 0 {
		return 0
	}
	var sum float64
	n := 0
	for _, r := range m.Representations {
		if r.Isolated {
			continue
		}
		sum += r.Quality
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// TotalMissingSegments counts missing (layer, segment) pairs.
func TotalMissingSegments(m *Manifest) int {
	if m == nil {
		return 0
	}
	n := 0
	for _, s := range m.Segments {
		n += len(s.MissingLayer)
	}
	return n
}

// RecoveredLayers counts representations that recovered at least once.
func RecoveredLayers(m *Manifest) int {
	if m == nil {
		return 0
	}
	n := 0
	for _, r := range m.Representations {
		if r.Recovered > 0 {
			n++
		}
	}
	return n
}
