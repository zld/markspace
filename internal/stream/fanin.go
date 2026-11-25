package stream

import (
	"sync"

	"packcheck/internal/segment"
)

// LayerOutput is everything one representation produced.
type LayerOutput struct {
	Representation string
	Packets        []segment.Packet
	Healthy        bool
}

// FanIn converges representation outputs onto one shared clock. It does not
// reinterpret timestamps: convergence means ordering by the RTP timestamp
// space every layer already shares, then producing a per-timestamp view.
type FanIn struct {
	mu      sync.Mutex
	outputs map[string]LayerOutput
	order   []string
}

// NewFanIn creates an empty convergence point.
func NewFanIn() *FanIn {
	return &FanIn{outputs: map[string]LayerOutput{}}
}

// Submit registers one layer's output.
func (f *FanIn) Submit(o LayerOutput) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, seen := f.outputs[o.Representation]; !seen {
		f.order = append(f.order, o.Representation)
	}
	f.outputs[o.Representation] = o
}

// HealthyLayers returns only still-usable representations, in submit order.
func (f *FanIn) HealthyLayers() []LayerOutput {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []LayerOutput
	for _, name := range f.order {
		if o := f.outputs[name]; o.Healthy {
			out = append(out, o)
		}
	}
	return out
}

// Signatures converts healthy outputs into segment boundary signatures.
func (f *FanIn) Signatures() ([]segment.LayerSignature, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sigs := make([]segment.LayerSignature, 0, len(f.order))
	for _, name := range f.order {
		o := f.outputs[name]
		ts, err := segment.PacketTimestamps(o.Packets)
		if err != nil {
			return nil, err
		}
		sigs = append(sigs, segment.LayerSignature{
			Representation: name, Timestamps: ts, Healthy: o.Healthy,
		})
	}
	return sigs, nil
}

// SharedTimestamps returns the timestamp sequence observed on every healthy
// layer, i.e. the converged shared-clock view. An empty result means no
// healthy layer remains.
func (f *FanIn) SharedTimestamps() []uint32 {
	healthy := f.HealthyLayers()
	if len(healthy) == 0 {
		return nil
	}
	base, err := segment.PacketTimestamps(healthy[0].Packets)
	if err != nil {
		return nil
	}
	common := make([]uint32, 0, len(base))
	for _, ts := range base {
		present := true
		for _, o := range healthy[1:] {
			ts2, err := segment.PacketTimestamps(o.Packets)
			if err != nil || !containsTS(ts2, ts) {
				present = false
				break
			}
		}
		if present {
			common = append(common, ts)
		}
	}
	return common
}

func containsTS(list []uint32, v uint32) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
