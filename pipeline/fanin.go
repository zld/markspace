package pipeline

import (
	"math"
	"sort"

	"rtpreplay/media"
)

// BoundaryMismatch records a failed cross-representation boundary check.
type BoundaryMismatch struct {
	ReferenceKbps int
	ComparedKbps  int
	Index         int
	RefTimeMS     float64
	CompareTimeMS float64
	DeltaMS       float64
	ToleranceMS   float64
}

// SyncResult is the fan-in verdict for one media family.
type SyncResult struct {
	Kind         string
	SyncErrorMS  float64 // maximum boundary discrepancy observed
	Failed       bool
	Mismatches   []BoundaryMismatch
	CheckedPairs int
}

// FanIn converges independently recovered representations onto the shared
// clock and cross-checks their boundaries. A discrepancy beyond one audio
// sample (8 kHz) or one video frame marks the family as failed.
func FanIn(reps []*Representation) *SyncResult {
	res := &SyncResult{Kind: familyKind(reps)}
	if len(reps) < 2 {
		return res // nothing to cross-check for a single representation
	}
	for i := 1; i < len(reps); i++ {
		ref, other := reps[0], reps[i]
		n := len(ref.Boundaries)
		if len(other.Boundaries) < n {
			n = len(other.Boundaries)
		}
		tol := toleranceMS(ref, other)
		for j := 0; j < n; j++ {
			refT := ref.Boundaries[j].TimeMS
			otherT := other.Boundaries[j].TimeMS
			delta := math.Abs(refT - otherT)
			res.CheckedPairs++
			if delta > tol {
				res.Failed = true
				res.Mismatches = append(res.Mismatches, BoundaryMismatch{
					ReferenceKbps: ref.BitrateKbps,
					ComparedKbps:  other.BitrateKbps,
					Index:         j,
					RefTimeMS:     refT,
					CompareTimeMS: otherT,
					DeltaMS:       delta,
					ToleranceMS:   tol,
				})
			}
			if delta > res.SyncErrorMS {
				res.SyncErrorMS = delta
			}
		}
	}
	return res
}

// toleranceMS returns the tighter of the two per-representation tolerances:
// one audio sample for audio, one median video frame for video.
func toleranceMS(a, b *Representation) float64 {
	ta := frameToleranceMS(a)
	tb := frameToleranceMS(b)
	if ta < tb {
		return ta
	}
	return tb
}

// frameToleranceMS is one media quantum on the representation's own clock.
func frameToleranceMS(r *Representation) float64 {
	if r.Kind == KindAudio {
		return media.TicksToMS(1, media.AudioClockRate) // 0.125 ms
	}
	if med, ok := medianFrameMS(r); ok {
		return med
	}
	// Fallback: one 30 fps frame.
	return 1000.0 / 30.0
}

// medianFrameMS derives the typical frame spacing from observed healthy
// samples, which stays valid under variable frame rates.
func medianFrameMS(r *Representation) (float64, bool) {
	pkts := r.Stream.Packets
	if len(pkts) < 2 {
		return 0, false
	}
	rate := r.Stream.ClockRate
	var deltas []float64
	prev := int64(-1)
	for _, p := range pkts {
		if p.Clock.Isolated {
			continue
		}
		if prev >= 0 {
			d := media.TicksToMS(p.Clock.Unwrapped-prev, rate)
			if d > 0 {
				deltas = append(deltas, d)
			}
		}
		prev = p.Clock.Unwrapped
	}
	if len(deltas) == 0 {
		return 0, false
	}
	sort.Float64s(deltas)
	return deltas[len(deltas)/2], true
}

func familyKind(reps []*Representation) string {
	for _, r := range reps {
		if r.Kind != "" {
			return r.Kind
		}
	}
	return ""
}

// LatencyP95MS computes the 95th percentile of per-packet arrival jitter:
// the deviation of each observed inter-packet arrival interval from the
// matching sender-clock interval, in milliseconds.
func LatencyP95MS(reps []*Representation) float64 {
	var samples []float64
	for _, r := range reps {
		pkts := r.Stream.Packets
		rate := r.Stream.ClockRate
		for i := 1; i < len(pkts); i++ {
			prev, cur := pkts[i-1], pkts[i]
			if prev.Clock.Isolated || cur.Clock.Isolated {
				continue
			}
			senderMS := media.TicksToMS(cur.Clock.Unwrapped-prev.Clock.Unwrapped, rate)
			arrivalMS := float64(cur.Arrival.Sub(prev.Arrival).Microseconds()) / 1000.0
			samples = append(samples, math.Abs(arrivalMS-senderMS))
		}
	}
	if len(samples) == 0 {
		return 0
	}
	sort.Float64s(samples)
	idx := int(math.Ceil(0.95*float64(len(samples)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}
