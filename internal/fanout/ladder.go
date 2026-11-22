// Package fanout expands one recovered input into independent ladder layers.
//
// Each layer keeps its own recovery state, its own sample list and its own
// segment boundaries. A failure in one layer is recorded against that layer
// only; it never changes another layer's data.
package fanout

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// LayerSpec describes one ladder representation.
type LayerSpec struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// BitrateKbps is the nominal target; it is metadata for the index.
	BitrateKbps int `json:"bitrate_kbps"`
}

// DefaultLadder returns a deterministic ladder of n layers derived from the
// source dimensions. Rungs halve the short side until it reaches 180p.
func DefaultLadder(n, srcWidth, srcHeight int) []LayerSpec {
	if n < 1 {
		n = 1
	}
	baseW, baseH := srcWidth, srcHeight
	if baseW <= 0 || baseH <= 0 {
		baseW, baseH = 1920, 1080
	}
	lays := make([]LayerSpec, 0, n)
	for i := 0; i < n; i++ {
		div := 1 << uint(n-1-i)
		w := evenInt(baseW / div)
		h := evenInt(baseH / div)
		if h < 180 {
			scale := float64(180) / float64(h)
			h = 180
			w = evenInt(int(float64(w) * scale))
		}
		if w < 2 {
			w = 2
		}
		if h < 2 {
			h = 2
		}
		// Bitrate scales with the pixel count and the rung index.
		pixels := w * h
		kbps := (pixels * 3) / 50000
		if kbps < 100 {
			kbps = 100
		}
		lays = append(lays, LayerSpec{
			Index:       i,
			Name:        fmt.Sprintf("rung-%d", i),
			Width:       w,
			Height:      h,
			BitrateKbps: kbps,
		})
	}
	return lays
}

func evenInt(v int) int {
	if v%2 == 1 {
		v--
	}
	if v < 2 {
		v = 2
	}
	return v
}

// LayerInput is the recovered material fed into the ladder.
type LayerInput struct {
	Track     model.TrackID
	Timescale uint32
	Samples   []model.Sample
	// Corruption marks the layer's fragment as damaged; samples referenced by
	// CorruptIndices are unusable.
	CorruptIndices map[int]bool
	// Weight overrides the default per-rung sample stride. Zero means the
	// ladder default (layer i keeps every 2^(n-1-i)-th sample).
	Weight int
	// CorruptLayer restricts CorruptIndices to one layer index. When negative,
	// the corruption applies to every layer.
	CorruptLayer int
}

// Layer is one independent representation with its own recovery outcome.
type Layer struct {
	Spec      LayerSpec      `json:"spec"`
	Track     model.TrackID  `json:"track"`
	Timescale uint32         `json:"timescale"`
	Available bool           `json:"available"`
	Samples   []model.Sample `json:"-"`
	// SampleIndices are the source sample indices retained, in order.
	SampleIndices []int `json:"sample_indices"`
	// SegmentStartTicks lists the first sample time of every planned segment.
	SegmentStartTicks []int64        `json:"segment_start_ticks"`
	Errors            []*model.Error `json:"-"`
	// Reason explains why the layer is unavailable.
	Reason string `json:"reason,omitempty"`
}

// Result is the complete fan-out.
type Result struct {
	Layers []Layer        `json:"layers"`
	Errors []*model.Error `json:"-"`
}

// Build creates every ladder layer. Layers are independent: a corrupt sample
// in one layer removes only that layer. If the lowest layer is destroyed the
// ladder still reports the remaining ones.
func Build(specs []LayerSpec, in LayerInput, segmentMS int64) (Result, error) {
	var res Result
	if len(specs) == 0 {
		return res, model.NewError(in.Track, model.StageFanout, model.CodeInvalidParameter, false, "empty ladder")
	}
	if in.Timescale == 0 {
		return res, model.NewError(in.Track, model.StageFanout, model.CodeInvalidParameter, false, "layer timescale must be positive")
	}
	if segmentMS <= 0 {
		return res, model.NewError(in.Track, model.StageFanout, model.CodeInvalidParameter, false, "segment duration must be positive")
	}
	segDur, err := clock.New(segmentMS, 1000)
	if err != nil {
		return res, err
	}
	segTicks, err := segDur.FloorTicks(in.Timescale)
	if err != nil {
		return res, err
	}

	ordered := append([]LayerSpec(nil), specs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })

	for pos, spec := range ordered {
		layer := Layer{Spec: spec, Track: in.Track, Timescale: in.Timescale, Available: true}
		layerCorrupt := in.CorruptIndices
		if in.CorruptLayer >= 0 && spec.Index != in.CorruptLayer {
			layerCorrupt = nil
		}
		stride := in.Weight
		if stride <= 0 {
			stride = 1 << uint(len(ordered)-1-pos)
		}
		if stride < 1 {
			stride = 1
		}
		for i, s := range in.Samples {
			if layerCorrupt[i] {
				layer.Available = false
				layer.Reason = fmt.Sprintf("source sample %d is corrupt", i)
				continue
			}
			if i%stride != 0 {
				continue
			}
			layer.Samples = append(layer.Samples, s)
			layer.SampleIndices = append(layer.SampleIndices, i)
		}
		if !layer.Available {
			layer.Samples = nil
			layer.SampleIndices = nil
			e := model.NewError(in.Track, model.StageFanout, model.CodeLayerUnavailable, true,
				"layer %s (%d) is unavailable: %s", spec.Name, spec.Index, layer.Reason)
			layer.Errors = append(layer.Errors, e)
			res.Errors = append(res.Errors, e)
			res.Layers = append(res.Layers, layer)
			continue
		}
		if len(layer.Samples) == 0 {
			layer.Available = false
			layer.Reason = "no samples survived downsampling"
			e := model.NewError(in.Track, model.StageFanout, model.CodeLayerUnavailable, true,
				"layer %s (%d) is unavailable: %s", spec.Name, spec.Index, layer.Reason)
			layer.Errors = append(layer.Errors, e)
			res.Errors = append(res.Errors, e)
			res.Layers = append(res.Layers, layer)
			continue
		}
		layer.SegmentStartTicks = segmentPlan(layer.Samples, segTicks, in.Timescale)
		res.Layers = append(res.Layers, layer)
	}
	return res, nil
}

// segmentPlan computes the first tick of each segment boundary for a layer.
// Boundaries are aligned to a segment grid expressed in ticks at the layer's
// timescale, starting at the layer's own first sample. Each layer's plan is
// derived only from its retained samples; the fan-in stage cross-checks that
// plans agree.
func segmentPlan(samples []model.Sample, segTicks int64, timescale uint32) []int64 {
	if len(samples) == 0 || segTicks <= 0 {
		return nil
	}
	var out []int64
	seen := map[int64]bool{}
	for _, s := range samples {
		t, err := clock.FromModel(s.Time.RepairedTime)
		if err != nil {
			continue
		}
		ticks, err := t.FloorTicks(timescale)
		if err != nil {
			continue
		}
		bucket := (ticks / segTicks) * segTicks
		if !seen[bucket] {
			seen[bucket] = true
			out = append(out, bucket)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
