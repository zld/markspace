package media

import "fmt"

// detectAnomalies records every region that violates the expected
// monotonic, fixed-stride timeline. The nominal stride is the track's
// regular frame cadence; one interval is emitted per discontinuity and
// classified as regression, overlap or gap. Atypical stts durations (a
// dropped/duplicated frame) are folded into the same region rather than
// reported twice.
func detectAnomalies(t *TrackTimeline, pts []int64, dur []uint32) {
	nominal := nominalStride(dur)
	reported := map[uint32]bool{}
	add := func(kind AnomalyKind, i uint32, start, end int64, detail string) {
		if reported[i] {
			return
		}
		reported[i] = true
		t.Anomalies = append(t.Anomalies, AnomalyInterval{
			Kind: kind, TrackID: t.TrackID,
			StartTick: start, EndTick: end,
			StartIndex: i, EndIndex: i, Detail: detail,
		})
	}
	// Compare every presentation step against the nominal stride. This
	// catches regressions, overlaps and gaps regardless of how the stts
	// run-length table encoded the surrounding durations.
	for i := 1; i < len(pts); i++ {
		actual := pts[i] - pts[i-1]
		idx := uint32(i)
		switch {
		case actual < 0:
			add(AnomalyRegression, idx, pts[i-1], pts[i],
				fmt.Sprintf("pts %d -> %d", pts[i-1], pts[i]))
		case actual < nominal:
			add(AnomalyOverlap, idx, pts[i], pts[i-1]+nominal,
				fmt.Sprintf("step %d < nominal %d", actual, nominal))
		case actual > nominal:
			add(AnomalyGap, idx, pts[i-1]+nominal, pts[i],
				fmt.Sprintf("gap %d ticks before pts %d", actual-nominal, pts[i]))
		}
	}
	// Surface any remaining atypical duration that did not produce a PTS
	// step anomaly, so a stts-only discontinuity is still untrusted.
	for i := range dur {
		if int64(dur[i]) != nominal {
			add(AnomalyGap, uint32(i), pts[i], pts[i]+int64(dur[i]),
				fmt.Sprintf("duration %d != nominal stride %d", dur[i], nominal))
		}
	}
	mergeAdjacent(t)
}

// mergeAdjacent folds same-kind intervals that touch or overlap into one
// region, so one physical discontinuity is reported exactly once.
func mergeAdjacent(t *TrackTimeline) {
	if len(t.Anomalies) < 2 {
		return
	}
	merged := t.Anomalies[:1]
	for _, a := range t.Anomalies[1:] {
		last := &merged[len(merged)-1]
		if a.Kind == last.Kind && a.StartTick <= last.EndTick {
			if a.EndTick > last.EndTick {
				last.EndTick = a.EndTick
			}
			if a.EndIndex > last.EndIndex {
				last.EndIndex = a.EndIndex
			}
			continue
		}
		merged = append(merged, a)
	}
	t.Anomalies = merged
}

// nominalStride returns the most common sample delta, i.e. the track's
// regular frame cadence rather than an average frame rate.
func nominalStride(dur []uint32) int64 {
	counts := map[uint32]int{}
	var best uint32
	for _, d := range dur {
		counts[d]++
		if counts[d] > counts[best] {
			best = d
		}
	}
	return int64(best)
}
