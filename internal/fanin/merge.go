// Package fanin joins the independent ladder layers onto one shared clock.
//
// The shared clock is the program timeline established from the earliest track
// origin. Fan-in verifies that every available layer's segment boundaries
// agree within an explicit tolerance; if they do not, index generation is
// refused rather than producing a misaligned index.
package fanin

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/fanout"
	"example.com/mediatimeline/internal/model"
)

// Options configures the merge.
type Options struct {
	// ToleranceTicks is the largest allowed difference between a layer's
	// segment boundary and the shared plan, expressed in the layer timescale.
	ToleranceTicks int64
	// Timescale is the shared program timescale.
	Timescale uint32
	// Segmentation is the output segment grid in ticks.
	SegmentTicks int64
	// RequireAllLayers makes any unavailable layer fatal. By default a
	// damaged layer is excluded and reported.
	RequireAllLayers bool
}

// Segment is a boundary-aligned program segment.
type Segment struct {
	Index     int   `json:"index"`
	StartTick int64 `json:"start_tick"`
	EndTick   int64 `json:"end_tick"`
}

// MergeResult is the outcome of fan-in.
type MergeResult struct {
	Segments       []Segment       `json:"segments"`
	Layers         []fanout.Layer  `json:"layers"`
	Recovered      []int           `json:"recovered_layers"`
	Unavailable    []int           `json:"unavailable_layers"`
	BoundaryErrors []BoundaryError `json:"boundary_errors"`
	Errors         []*model.Error  `json:"-"`
}

// BoundaryError describes one layer's disagreement with the shared plan.
type BoundaryError struct {
	Layer        int   `json:"layer"`
	ExpectedTick int64 `json:"expected_tick"`
	ActualTick   int64 `json:"actual_tick"`
	Delta        int64 `json:"delta"`
}

// Merge validates the layers against the shared segment plan.
func Merge(result fanout.Result, opts Options) (MergeResult, error) {
	var out MergeResult
	if opts.Timescale == 0 || opts.SegmentTicks <= 0 {
		return out, model.NewError("", model.StageFanin, model.CodeInvalidParameter, false,
			"fan-in needs a positive timescale and segment length")
	}
	if opts.ToleranceTicks < 0 {
		return out, model.NewError("", model.StageFanin, model.CodeInvalidParameter, false,
			"tolerance must not be negative")
	}
	layers := append([]fanout.Layer(nil), result.Layers...)
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].Spec.Index < layers[j].Spec.Index })

	// The shared plan is the union of every available layer's boundaries,
	// snapped to the grid. A layer that has no samples is unavailable.
	plan := map[int64]bool{}
	var recoverable []fanout.Layer
	for _, l := range layers {
		out.Layers = append(out.Layers, l)
		if !l.Available {
			out.Unavailable = append(out.Unavailable, l.Spec.Index)
			if opts.RequireAllLayers {
				e := model.NewError(l.Track, model.StageFanin, model.CodeLayerUnavailable, false,
					"layer %d unavailable and all layers are required: %s", l.Spec.Index, l.Reason)
				out.Errors = append(out.Errors, e)
				return out, e
			}
			continue
		}
		recoverable = append(recoverable, l)
		out.Recovered = append(out.Recovered, l.Spec.Index)
		for _, b := range l.SegmentStartTicks {
			plan[snap(b, opts.SegmentTicks)] = true
		}
	}
	if len(recoverable) == 0 {
		e := model.NewError("", model.StageFanin, model.CodeLayerUnavailable, false, "no layer is available")
		out.Errors = append(out.Errors, e)
		return out, e
	}
	// The plan is anchored at zero: the program clock starts at the common
	// origin, and a layer whose first sample is late is a boundary fault.
	if !plan[0] {
		e := model.NewError(recoverable[0].Track, model.StageFanin, model.CodeBoundaryMismatch, false,
			"the shared plan does not start at tick 0")
		out.Errors = append(out.Errors, e)
		return out, e
	}

	// The plan grid must start at zero and be contiguous using the configured
	// segment length. A hole in the union reveals a layer that skipped a whole
	// segment; that is a boundary error, not a new plan point.
	start, end := int64(0), int64(0)
	for b := range plan {
		if b < start {
			start = b
		}
		if b > end {
			end = b
		}
	}
	var segs []Segment
	for t := start; t <= end; t += opts.SegmentTicks {
		if !plan[t] {
			out.Errors = append(out.Errors, model.NewError("", model.StageFanin, model.CodeBoundaryMismatch, true,
				"segment starting at tick %d has no samples", t))
		}
		segs = append(segs, Segment{StartTick: t, EndTick: t + opts.SegmentTicks})
	}
	for i := range segs {
		segs[i].Index = i
	}
	out.Segments = segs

	// Every layer must place its boundaries on the shared plan within
	// tolerance. This is the invariant that protects the index.
	for _, l := range recoverable {
		for _, b := range l.SegmentStartTicks {
			snapped := snap(b, opts.SegmentTicks)
			delta := b - snapped
			if delta < 0 {
				delta = -delta
			}
			if delta > opts.ToleranceTicks {
				out.BoundaryErrors = append(out.BoundaryErrors, BoundaryError{
					Layer: l.Spec.Index, ExpectedTick: snapped, ActualTick: b, Delta: delta,
				})
			}
		}
	}
	if len(out.BoundaryErrors) > 0 {
		for _, be := range out.BoundaryErrors {
			out.Errors = append(out.Errors, model.NewError(layerTrack(layers, be.Layer), model.StageFanin, model.CodeBoundaryMismatch, false,
				"layer %d boundary at tick %d differs from shared tick %d by %d (tolerance %d)",
				be.Layer, be.ActualTick, be.ExpectedTick, be.Delta, opts.ToleranceTicks))
		}
		e := out.Errors[len(out.Errors)-1]
		return out, e
	}

	// Layers must cover the same number of segments; a short layer would make
	// the index non-random-access at the tail.
	want := len(segs)
	for _, l := range recoverable {
		if got := len(l.SegmentStartTicks); got < want {
			e := model.NewError(l.Track, model.StageFanin, model.CodeBoundaryMismatch, false,
				"layer %d covers %d of %d segments", l.Spec.Index, got, want)
			out.Errors = append(out.Errors, e)
			return out, e
		}
	}
	return out, nil
}

func layerTrack(layers []fanout.Layer, idx int) model.TrackID {
	for _, l := range layers {
		if l.Spec.Index == idx {
			return l.Track
		}
	}
	return ""
}

// snap rounds tick down to the nearest multiple of grid.
func snap(tick, grid int64) int64 {
	if grid <= 0 {
		return tick
	}
	q := tick / grid
	if tick < 0 && tick%grid != 0 {
		q--
	}
	return q * grid
}

// SharedOrigin returns the earliest repaired start across all available layers
// as an exact rational time. It is the program clock the index is built on.
func SharedOrigin(result fanout.Result) (clock.Rational, error) {
	best := clock.Zero
	found := false
	for _, l := range result.Layers {
		if !l.Available || len(l.Samples) == 0 {
			continue
		}
		t, err := clock.FromModel(l.Samples[0].Time.RepairedTime)
		if err != nil {
			return clock.Zero, err
		}
		if !found || t.Cmp(best) < 0 {
			best, found = t, true
		}
	}
	if !found {
		return clock.Zero, fmt.Errorf("no available layer")
	}
	return best, nil
}
