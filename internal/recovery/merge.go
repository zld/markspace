package recovery

import (
	"fmt"
	"sort"

	"archivecheck/internal/media"
)

// Merge fans several recovered layers into one shared timeline on the common
// millisecond base. Every layer's evidence is preserved: a disagreement beyond
// tolerance becomes a conflict listing each layer's boundary, never a majority
// vote that discards the outlier.
type Merge struct {
	ToleranceMs      int64
	Layers           []string
	Boundaries       []SharedBoundary
	Conflicts        []Conflict
	Faults           []media.Fault
	DriftMs          int64
	UnrecoveredSpans []Gap
}

// SharedBoundary is one aligned boundary and what each layer observed.
type SharedBoundary struct {
	RefMs   int64
	Times   map[string]int64
	Offsets map[string]int64
	Spread  int64
}

// Conflict is a boundary whose layer spread exceeds tolerance.
type Conflict struct {
	RefMs     int64
	Tolerance int64
	Spread    int64
	Times     map[string]int64
	Offsets   map[string]int64
}

// LayerDiffs reports, per layer, the largest absolute deviation from the
// earliest observed time at any shared boundary.
func (m Merge) LayerDiffs() map[string]int64 {
	diffs := map[string]int64{}
	for _, b := range m.Boundaries {
		for layer, t := range b.Times {
			d := t - b.RefMs
			if d < 0 {
				d = -d
			}
			if cur, ok := diffs[layer]; !ok || d > cur {
				diffs[layer] = d
			}
		}
	}
	return diffs
}

// SegmentCount totals recovered segments across layers.
func SegmentCount(results []Result) int {
	n := 0
	for _, r := range results {
		n += len(r.Segments)
	}
	return n
}

type anchorObs struct {
	layer  string
	timeMs int64
	offset int64
}

// MergeResult aligns layer anchors and retained gaps on the shared clock.
func MergeResult(results []Result, toleranceMs int64) (Merge, error) {
	if toleranceMs <= 0 {
		return Merge{}, fmt.Errorf("recovery: tolerance must be positive")
	}
	if len(results) == 0 {
		return Merge{}, fmt.Errorf("recovery: no layers to merge")
	}
	seen := map[string]bool{}
	layers := make([]string, 0, len(results))
	for _, r := range results {
		if r.Layer == "" {
			return Merge{}, fmt.Errorf("recovery: layer with empty name")
		}
		if seen[r.Layer] {
			return Merge{}, fmt.Errorf("recovery: duplicate layer %q", r.Layer)
		}
		seen[r.Layer] = true
		layers = append(layers, r.Layer)
	}
	sort.Strings(layers)
	m := Merge{ToleranceMs: toleranceMs, Layers: layers}

	all := make([]anchorObs, 0, len(layers)*8)
	for _, r := range results {
		prev := int64(-1 << 62)
		for _, a := range r.Anchors {
			if a.TimeMs < prev {
				return Merge{}, fmt.Errorf("recovery: layer %s anchors are not monotonic across the shared clock", r.Layer)
			}
			prev = a.TimeMs
			all = append(all, anchorObs{layer: r.Layer, timeMs: a.TimeMs, offset: a.Offset})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].timeMs != all[j].timeMs {
			return all[i].timeMs < all[j].timeMs
		}
		return all[i].layer < all[j].layer
	})

	cluster := []anchorObs{}
	flush := func() {
		if len(cluster) == 0 {
			return
		}
		b := SharedBoundary{RefMs: cluster[0].timeMs, Times: map[string]int64{}, Offsets: map[string]int64{}}
		earliestPerLayer := map[string]anchorObs{}
		for _, o := range cluster {
			if prev, ok := earliestPerLayer[o.layer]; !ok || o.timeMs < prev.timeMs {
				earliestPerLayer[o.layer] = o
			}
		}
		maxT := b.RefMs
		for layer, o := range earliestPerLayer {
			b.Times[layer] = o.timeMs
			b.Offsets[layer] = o.offset
			if o.timeMs > maxT {
				maxT = o.timeMs
			}
		}
		b.Spread = maxT - b.RefMs
		m.Boundaries = append(m.Boundaries, b)
		if b.Spread > toleranceMs {
			m.Conflicts = append(m.Conflicts, Conflict{RefMs: b.RefMs, Tolerance: toleranceMs, Spread: b.Spread,
				Times: copyMap(b.Times), Offsets: copyMap(b.Offsets)})
			m.Faults = append(m.Faults, media.NewFault(media.KindBoundaryConflict, "",
				fmt.Sprintf("boundary at %d ms spreads %d ms across layers (%s) with tolerance %d ms",
					b.RefMs, b.Spread, formatTimes(b.Times), toleranceMs), -1, media.Recoverable))
		}
		if b.Spread > m.DriftMs {
			m.DriftMs = b.Spread
		}
		cluster = cluster[:0]
	}
	for _, o := range all {
		if len(cluster) > 0 && o.timeMs-cluster[0].timeMs > toleranceMs {
			flush()
		}
		cluster = append(cluster, o)
	}
	flush()

	// Union retained gaps across layers into shared unrecovered spans.
	spans := map[[2]int64]Gap{}
	for _, r := range results {
		for _, g := range r.Gaps {
			key := [2]int64{g.StartMs, g.EndMs}
			if prev, ok := spans[key]; ok {
				prev.Detail = prev.Detail + "; " + r.Layer + ": " + g.Detail
				spans[key] = prev
				continue
			}
			g.Detail = r.Layer + ": " + g.Detail
			spans[key] = g
		}
	}
	for _, g := range spans {
		m.UnrecoveredSpans = append(m.UnrecoveredSpans, g)
	}
	sort.SliceStable(m.UnrecoveredSpans, func(i, j int) bool {
		a, b := m.UnrecoveredSpans[i], m.UnrecoveredSpans[j]
		if a.StartMs != b.StartMs {
			return a.StartMs < b.StartMs
		}
		return a.EndMs < b.EndMs
	})
	media.SortFaults(m.Faults)
	return m, nil
}

func copyMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func formatTimes(times map[string]int64) string {
	layers := make([]string, 0, len(times))
	for l := range times {
		layers = append(layers, l)
	}
	sort.Strings(layers)
	out := ""
	for i, l := range layers {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%s=%dms", l, times[l])
	}
	return out
}
