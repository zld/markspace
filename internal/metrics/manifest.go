package metrics

import (
	"fmt"
	"math"

	"mediainspect/internal/stream"
)

// ManifestStats summarizes how well keyframes align to manifest segment
// boundaries on the unified timebase.
type ManifestStats struct {
	StepMs         float64 `json:"manifest_step_ms"`
	Segments       int     `json:"manifest_segments"`
	MaxAlignmentMs float64 `json:"manifest_alignment_max_ms"`
}

// ManifestAlignment measures, for every segment boundary of stepMs within
// the media duration, the distance to the nearest keyframe timestamp.
func ManifestAlignment(keyframeMs []float64, stepMs, durationMs float64) ManifestStats {
	st := ManifestStats{StepMs: stepMs}
	if stepMs <= 0 || len(keyframeMs) == 0 {
		return st
	}
	for b := stepMs; b < durationMs; b += stepMs {
		st.Segments++
		nearest := math.Inf(1)
		for _, k := range keyframeMs {
			if d := math.Abs(k - b); d < nearest {
				nearest = d
			}
		}
		if nearest > st.MaxAlignmentMs {
			st.MaxAlignmentMs = nearest
		}
	}
	return st
}

// Invariants verifies cross-stage conservation rules.
type Invariants struct {
	OK         bool     `json:"ok"`
	Violations []string `json:"violations,omitempty"`
}

// CheckInvariants validates that the index digest is stable across stages,
// that frame counts conserve (input = output + duplicates + late drops) and
// that the media duration derived from execution matches the scanned
// duration within toleranceMs.
func CheckInvariants(scanDigest, execDigest string, inputFrames int, st stream.ExecStats, scanDurationMs, execDurationMs, toleranceMs float64) Invariants {
	inv := Invariants{OK: true}
	fail := func(format string, args ...interface{}) {
		inv.OK = false
		inv.Violations = append(inv.Violations, fmt.Sprintf(format, args...))
	}
	if scanDigest != execDigest {
		fail("index digest changed between stages: %s != %s", scanDigest, execDigest)
	}
	if got := st.OutputFrames + st.Duplicates + st.LateDropped; got != inputFrames {
		fail("frame count not conserved: output+dup+late=%d input=%d", got, inputFrames)
	}
	if d := math.Abs(scanDurationMs - execDurationMs); d > toleranceMs {
		fail("media duration drift: scan=%.3fms exec=%.3fms", scanDurationMs, execDurationMs)
	}
	return inv
}
