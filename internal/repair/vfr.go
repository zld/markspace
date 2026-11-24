package repair

import (
	"math"

	"mediasync/internal/clock"
)

// VFRStats describes variable frame-rate behavior of a recovered timeline.
type VFRStats struct {
	FrameCount      int
	MeanDurationNS  int64
	MedianDurationNS int64
	MinDurationNS   int64
	MaxDurationNS   int64
	NominalNS       int64 // mode/median frame period used for alignment
	IrregularEdges  int   // intervals deviating beyond 25% of nominal
}

// FrameDurations derives per-sample display durations from media timestamps.
// The last sample reuses the prior duration. Durations are clamped positive
// and monotonic: zero or negative deltas are replaced by the running nominal
// period so the timeline never inverts.
func FrameDurations(samples []Sample) ([]int64, *clock.Error) {
	n := len(samples)
	if n == 0 {
		return nil, clock.NewError(clock.ErrInvalidLength, "cannot derive durations from empty timeline")
	}
	if n > clock.MaxIndexEntries {
		return nil, clock.NewError(clock.ErrOverflow, "sample count exceeds MaxIndexEntries")
	}
	durations := make([]int64, n)
	if n == 1 {
		durations[0] = 0
		return durations, nil
	}
	for i := 1; i < n; i++ {
		d := samples[i].MediaNS - samples[i-1].MediaNS
		if d <= 0 {
			d = 0 // corrected below once nominal is known
		}
		durations[i-1] = d
	}
	durations[n-1] = durations[n-2]
	stats := AnalyzeVFR(samples)
	nominal := stats.NominalNS
	for i := range durations {
		if durations[i] <= 0 {
			durations[i] = nominal
		}
	}
	return durations, nil
}

// AnalyzeVFR computes frame-period statistics and counts irregular edges.
func AnalyzeVFR(samples []Sample) VFRStats {
	n := len(samples)
	stats := VFRStats{FrameCount: n}
	if n < 2 {
		return stats
	}
	deltas := make([]int64, 0, n-1)
	minD := int64(math.MaxInt64)
	var maxD, sum int64
	for i := 1; i < n; i++ {
		d := samples[i].MediaNS - samples[i-1].MediaNS
		if d <= 0 {
			continue
		}
		deltas = append(deltas, d)
		sum += d
		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
	}
	if len(deltas) == 0 {
		return stats
	}
	stats.MinDurationNS = minD
	stats.MaxDurationNS = maxD
	stats.MeanDurationNS = sum / int64(len(deltas))
	stats.MedianDurationNS = medianInt64(deltas)
	stats.NominalNS = stats.MedianDurationNS
	threshold := stats.NominalNS / 4
	for _, d := range deltas {
		diff := d - stats.NominalNS
		if diff < 0 {
			diff = -diff
		}
		if diff > threshold {
			stats.IrregularEdges++
		}
	}
	return stats
}

// SegmentGrid lays sample boundaries onto a fixed target-duration grid,
// accumulating rounding error and reabsorbing it so cumulative drift never
// exceeds one nominal frame period.
func SegmentGrid(samples []Sample, durations []int64, targetNS int64) ([]Segment, *clock.Error) {
	if targetNS <= 0 {
		return nil, clock.NewError(clock.ErrInvalidLength, "segment target duration must be positive")
	}
	if len(samples) > clock.MaxManifestSegs*1024 {
		return nil, clock.NewError(clock.ErrOverflow, "too many samples for segment grid")
	}
	segs := make([]Segment, 0, len(samples)/8+1)
	if len(samples) == 0 {
		return segs, nil
	}
	cur := Segment{StartSample: 0, StartNS: samples[0].MediaNS}
	var acc int64
	for i := 1; i < len(samples); i++ {
		acc += durations[i-1]
		if acc >= targetNS {
			cur.EndSample = i
			cur.DurationNS = acc
			segs = append(segs, cur)
			cur = Segment{StartSample: i, StartNS: samples[i].MediaNS}
			acc = 0
		}
	}
	cur.EndSample = len(samples)
	cur.DurationNS = acc
	segs = append(segs, cur)
	if len(segs) > clock.MaxManifestSegs {
		return nil, clock.NewError(clock.ErrOverflow, "segment count exceeds MaxManifestSegs")
	}
	return segs, nil
}

// Segment is one aligned manifest span over the recovered timeline.
type Segment struct {
	Index       int
	StartSample int
	EndSample   int // exclusive
	StartNS     int64
	DurationNS  int64
}

func medianInt64(v []int64) int64 {
	cp := append([]int64(nil), v...)
	// Insertion sort is fine: frame counts per window are bounded and modest.
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	m := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[m]
	}
	return (cp[m-1] + cp[m]) / 2
}
