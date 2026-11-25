package segment

import (
	"fmt"

	"mediad/internal/model"
)

// Align projects every representation onto one segment grid. All
// representations share the same model.TimeBase, so their nominal boundaries
// are identical; this type measures the real per-boundary drift produced by
// packetized delivery and rejects anything larger than one packet duration,
// the maximum error allowed by the specification.
type Align struct {
	base        model.TimeBase
	packetTicks int64
	// boundary[segmentIndex] maps representation id to the observed media
	// time of its first packet in that segment.
	boundary map[int]map[string]int64
	maxDrift int64
}

// NewAlign creates an aligner. packetTicks is one input packet's duration.
func NewAlign(base model.TimeBase, packetTicks int64) *Align {
	return &Align{
		base:        base,
		packetTicks: packetTicks,
		boundary:    make(map[int]map[string]int64),
	}
}

// Observe records where representation repID actually started segment idx.
// Only the first observation per (idx, repID) is kept.
func (a *Align) Observe(idx int, repID string, mediaTicks int64) {
	row, ok := a.boundary[idx]
	if !ok {
		row = make(map[string]int64)
		a.boundary[idx] = row
	}
	if _, seen := row[repID]; seen {
		return
	}
	row[repID] = mediaTicks
	a.recomputeDrift(idx)
}

// recomputeDrift updates the maximum cross-representation drift for one
// segment index from its observed boundary times.
func (a *Align) recomputeDrift(idx int) {
	row := a.boundary[idx]
	var min, max int64
	first := true
	for _, t := range row {
		if first || t < min {
			min = t
		}
		if first || t > max {
			max = t
		}
		first = false
	}
	if d := max - min; d > a.maxDrift {
		a.maxDrift = d
	}
}

// MaxDriftTicks returns the largest boundary difference observed across all
// representations and segments.
func (a *Align) MaxDriftTicks() int64 { return a.maxDrift }

// Validate fails when the measured drift exceeds one packet duration.
func (a *Align) Validate() error {
	if a.maxDrift > a.packetTicks {
		return fmt.Errorf("segment: alignment drift %d ticks exceeds one packet duration %d",
			a.maxDrift, a.packetTicks)
	}
	return nil
}

// BoundaryCount returns how many segment indices have been observed.
func (a *Align) BoundaryCount() int { return len(a.boundary) }
