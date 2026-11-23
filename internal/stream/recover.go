package stream

import (
	"sync"

	"mediad/internal/clock"
)

// Boundary is one shared segment boundary expressed in master-clock ticks.
type Boundary struct {
	Index int
	Start int64 // inclusive tick
	End   int64 // exclusive tick
}

// Recoverer tracks per-layer progress against the shared segment boundaries
// and isolates/recovers a single layer without touching the others. Recovery
// always resumes from the most recent confirmed boundary, never mid-segment.
type Recoverer struct {
	mu         sync.Mutex
	boundaries []Boundary
	// confirmed[layer] = index of the last fully delivered boundary.
	confirmed []int
	// awaiting[layer] = boundary index the layer is currently filling.
	awaiting   []int
	damaged    []bool
	recoveries []int
	// alignErrMS[layer] = largest observed boundary alignment error in ms.
	alignErrMS []int64
	tickRate   uint32 // master-clock ticks per second, for ms conversion
}

// NewRecoverer creates a recoverer for n layers and the shared boundaries.
func NewRecoverer(n int, boundaries []Boundary, tickRate uint32) *Recoverer {
	r := &Recoverer{
		boundaries: boundaries,
		confirmed:  make([]int, n),
		awaiting:   make([]int, n),
		damaged:    make([]bool, n),
		recoveries: make([]int, n),
		alignErrMS: make([]int64, n),
		tickRate:   tickRate,
	}
	for i := range r.awaiting {
		r.awaiting[i] = 0
		r.confirmed[i] = -1
	}
	return r
}

// MarkDamaged isolates a layer after a corruption; other layers proceed.
func (r *Recoverer) MarkDamaged(layer int) {
	r.mu.Lock()
	if layer >= 0 && layer < len(r.damaged) {
		r.damaged[layer] = true
	}
	r.mu.Unlock()
}

// IsDamaged reports a layer's isolation state.
func (r *Recoverer) IsDamaged(layer int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return layer >= 0 && layer < len(r.damaged) && r.damaged[layer]
}

// Recover restores a damaged layer, resetting its in-flight boundary to the
// most recently confirmed one so it resumes on a clean segment edge. It
// returns the boundary index the layer resumes from.
func (r *Recoverer) Recover(layer int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if layer < 0 || layer >= len(r.damaged) {
		return 0, clock.TrackError(clock.StageStream, uint32(layer+1),
			"recover: layer index out of range", false)
	}
	if !r.damaged[layer] {
		return r.awaiting[layer], nil
	}
	r.damaged[layer] = false
	r.recoveries[layer]++
	// Resume just after the last confirmed boundary.
	resume := r.confirmed[layer] + 1
	if resume >= len(r.boundaries) {
		resume = len(r.boundaries) - 1
	}
	if resume < 0 {
		resume = 0
	}
	r.awaiting[layer] = resume
	return resume, nil
}

// RecordDelivery marks a packet delivered for a layer at master tick ts.
// When the packet reaches the end of the awaited boundary that boundary is
// confirmed. The observed cross-layer alignment error (vs the ideal edge) is
// recorded in integer milliseconds.
func (r *Recoverer) RecordDelivery(layer int, ts int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if layer < 0 || layer >= len(r.confirmed) {
		return clock.TrackError(clock.StageStream, uint32(layer+1),
			"delivery: layer index out of range", false)
	}
	if r.damaged[layer] {
		return clock.TrackError(clock.StageStream, uint32(layer+1),
			"delivery to damaged layer", true)
	}
	ai := r.awaiting[layer]
	if ai >= len(r.boundaries) {
		return nil
	}
	b := r.boundaries[ai]
	// Integer alignment error against the ideal boundary end, in ms.
	if ts >= b.End {
		errTicks := ts - b.End
		if errTicks < 0 {
			errTicks = -errTicks
		}
		errMS := errTicks * 1000 / int64(r.tickRate)
		if errMS > r.alignErrMS[layer] {
			r.alignErrMS[layer] = errMS
		}
		r.confirmed[layer] = ai
		r.awaiting[layer] = ai + 1
	}
	return nil
}

// LayerState is a point-in-time recovery snapshot.
type LayerState struct {
	Layer      int
	Damaged    bool
	Confirmed  int
	Awaiting   int
	Recoveries int
	AlignErrMS int64
}

// Snapshot returns all layers' recovery state.
func (r *Recoverer) RecovererStats() []LayerState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LayerState, len(r.damaged))
	for i := range r.damaged {
		out[i] = LayerState{
			Layer: i, Damaged: r.damaged[i], Confirmed: r.confirmed[i],
			Awaiting: r.awaiting[i], Recoveries: r.recoveries[i],
			AlignErrMS: r.alignErrMS[i],
		}
	}
	return out
}

// TotalRecovered counts layers that completed at least one recovery.
func (r *Recoverer) TotalRecovered() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.recoveries {
		if c > 0 {
			n++
		}
	}
	return n
}

// MaxAlignErrorMS returns the worst alignment error across layers.
func (r *Recoverer) MaxAlignErrorMS() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var max int64
	for _, v := range r.alignErrMS {
		if v > max {
			max = v
		}
	}
	return max
}
