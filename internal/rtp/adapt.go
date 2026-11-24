package rtp

import (
	"archive/internal/clock"
)

// Layer is one representation offered for recovery.
type Layer struct {
	Name            string
	TargetKbps      int
	FrameBytes      int // nominal bytes per frame
	FrameIntervalNS int64
	// TSStep is the RTP timestamp increment per frame.
	TSStep uint32
	// PacketsPerFrame splits each frame across this many RTP packets.
	PacketsPerFrame int
}

// BitratePlan is the deterministic bitrate state for one layer after applying
// the observed bandwidth series.
type BitratePlan struct {
	Layer         string
	TargetKbps    int
	ActualKbps    float64
	DeviationPct  float64
	FrameCount    int
	PayloadBytes  int
	MeasuredKbps  float64
	IntendedBytes int
}

// BytesPerFrame is the intended payload size per frame implied by the target
// bitrate and this layer's frame interval.
func (p BitratePlan) BytesPerFrame() int {
	if p.FrameCount <= 0 {
		return 0
	}
	return p.IntendedBytes / p.FrameCount
}

// ChooseLayers selects the highest-value representation whose target fits the
// available bandwidth. When nothing fits, the lowest layer is still offered so
// a degraded layer can be isolated later instead of silently producing nothing.
func ChooseLayers(layers []Layer, bandwidthKbps int) []Layer {
	if len(layers) == 0 {
		return nil
	}
	best := -1
	for i, l := range layers {
		if l.TargetKbps <= bandwidthKbps {
			if best == -1 || l.TargetKbps > layers[best].TargetKbps {
				best = i
			}
		}
	}
	if best == -1 {
		best = 0
		for i := range layers {
			if layers[i].TargetKbps < layers[best].TargetKbps {
				best = i
			}
		}
	}
	return []Layer{layers[best]}
}

// ActualLayerPlan computes what one layer actually produces given how many
// packets survived recovery. Loss lowers the actual bitrate predictably, which
// the caller reports as deviation from target.
func ActualLayerPlan(l Layer, rec *Result, durationNS int64) (BitratePlan, error) {
	if l.TargetKbps <= 0 {
		return BitratePlan{}, clock.Newf(clock.KindInvalidMetadata, "layer %s target %d kbps is not positive", l.Name, l.TargetKbps)
	}
	if durationNS <= 0 {
		return BitratePlan{}, clock.Newf(clock.KindInvalidMetadata, "layer %s duration %d ns is not positive", l.Name, durationNS)
	}
	if l.FrameIntervalNS <= 0 {
		return BitratePlan{}, clock.Newf(clock.KindInvalidMetadata, "layer %s frame interval is not positive", l.Name)
	}
	frames := int(durationNS / l.FrameIntervalNS)
	if frames <= 0 {
		frames = 1
	}
	delivered := int64(len(rec.Ordered))
	expected := delivered + int64(rec.Lost)
	payloadBytes := 0
	for _, p := range rec.Ordered {
		payloadBytes += len(p.Payload)
	}
	seconds := float64(durationNS) / float64(clock.NanosPerSecond)
	actualKbps := float64(payloadBytes*8) / seconds / 1000.0
	// The measured figure includes only surviving payload; scale to the target
	// so the reported actual reflects both intended rate and loss.
	keepRatio := 1.0
	if expected > 0 {
		keepRatio = float64(delivered) / float64(expected)
		if keepRatio > 1 {
			keepRatio = 1
		}
	}
	scaled := float64(l.TargetKbps) * keepRatio
	dev := 0.0
	if l.TargetKbps > 0 {
		dev = (scaled - float64(l.TargetKbps)) / float64(l.TargetKbps) * 100.0
		if dev < 0 {
			dev = -dev
		}
	}
	return BitratePlan{
		Layer:         l.Name,
		TargetKbps:    l.TargetKbps,
		ActualKbps:    actualKbps,
		MeasuredKbps:  actualKbps,
		DeviationPct:  dev,
		FrameCount:    frames,
		PayloadBytes:  payloadBytes,
		IntendedBytes: int(float64(l.TargetKbps) * 1000 / 8 * seconds),
	}, nil
}

// Scheduler applies a changing bandwidth series over time. Each sample lasts
// an equal share of the session so the plan is reproducible from the series
// alone.
type Scheduler struct {
	Samples []int
}

// Plan assigns a layer to every sample, preferring the highest layer that fits
// that sample's bandwidth.
func (s Scheduler) Plan(layers []Layer) ([]string, error) {
	if len(layers) == 0 {
		return nil, clock.New(clock.KindInvalidMetadata, "no layers configured")
	}
	if len(s.Samples) == 0 {
		return nil, clock.New(clock.KindInvalidMetadata, "no bandwidth samples")
	}
	for _, b := range s.Samples {
		if b <= 0 {
			return nil, clock.Newf(clock.KindOutOfRange, "bandwidth sample %d kbps is not positive", b)
		}
	}
	out := make([]string, len(s.Samples))
	for i, b := range s.Samples {
		out[i] = ChooseLayers(layers, b)[0].Name
	}
	return out, nil
}

// DominantLayer returns the layer selected for the highest bandwidth sample,
// which is the layer the shared-clock fan-in validates by default.
func (s Scheduler) DominantLayer(layers []Layer) (Layer, error) {
	if len(s.Samples) == 0 {
		return Layer{}, clock.New(clock.KindInvalidMetadata, "no bandwidth samples")
	}
	max := s.Samples[0]
	for _, b := range s.Samples[1:] {
		if b > max {
			max = b
		}
	}
	chosen := ChooseLayers(layers, max)
	if len(chosen) == 0 {
		return Layer{}, clock.New(clock.KindInvalidMetadata, "no layer fits maximum bandwidth")
	}
	return chosen[0], nil
}
