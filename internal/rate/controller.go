// Package rate allocates the total bitrate budget across segments and drives
// the bounded closed-loop correction of per-segment encodes.
package rate

import (
	"fmt"
	"math"
)

// AllocationError marks an infeasible budget so callers can distinguish it
// from encoding failures.
type AllocationError struct{ Reason string }

func (e *AllocationError) Error() string { return "rate allocation infeasible: " + e.Reason }

// Allocate distributes totalTargetBits across n segments so the integer sum of
// returned budgets equals totalTargetBits exactly. Each segment's weight is its
// complexity scaled by its frame count (short tails therefore claim less).
//
// A per-segment minimum of minSegmentBits is enforced. If even equal minimums
// exceed the total, an AllocationError is returned.
func Allocate(totalTargetBits int64, complexities []float64, frameCounts []int,
	minSegmentBits int64) ([]int64, error) {
	n := len(complexities)
	if n == 0 {
		return nil, &AllocationError{Reason: "no segments to budget"}
	}
	if len(frameCounts) != n {
		return nil, &AllocationError{Reason: "frame count / complexity length mismatch"}
	}
	if totalTargetBits <= 0 {
		return nil, &AllocationError{Reason: "total target budget must be positive"}
	}
	if minSegmentBits <= 0 {
		return nil, &AllocationError{Reason: "minimum segment budget must be positive"}
	}
	if totalTargetBits < minSegmentBits*int64(n) {
		return nil, &AllocationError{Reason: fmt.Sprintf(
			"target %d bits cannot cover %d segments at minimum %d bits each (need %d)",
			totalTargetBits, n, minSegmentBits, minSegmentBits*int64(n))}
	}

	weights := make([]float64, n)
	weightSum := 0.0
	for i := range complexities {
		// Floor complexity at 1 so static segments still receive budget.
		c := complexities[i]
		if c < 1 {
			c = 1
		}
		weights[i] = c * float64(frameCounts[i])
		weightSum += weights[i]
	}

	budgets := make([]int64, n)
	var allocated int64
	for i := range weights {
		b := int64(math.Round(float64(totalTargetBits) * weights[i] / weightSum))
		if b < minSegmentBits {
			b = minSegmentBits
		}
		budgets[i] = b
		allocated += b
	}

	// Correct the rounding/minimum gap while preserving every floor, using the
	// raw fractional weights to decide who gives or receives a bit.
	delta := totalTargetBits - allocated
	if err := rebalance(budgets, weights, minSegmentBits, totalTargetBits, delta); err != nil {
		return nil, err
	}

	var sum int64
	for _, b := range budgets {
		sum += b
	}
	if sum != totalTargetBits {
		return nil, &AllocationError{Reason: fmt.Sprintf("internal: budgets sum to %d, want %d", sum, totalTargetBits)}
	}
	return budgets, nil
}

// rebalance fixes an over/under allocation by moving whole bits between
// segments, never below the minimum and always along weight order.
func rebalance(budgets []int64, weights []float64, minBits, total int64, delta int64) error {
	order := make([]int, len(budgets))
	for i := range order {
		order[i] = i
	}
	// Simple insertion sort by weight: segment counts are small.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && weights[order[j-1]] < weights[order[j]]; j-- {
			order[j-1], order[j] = order[j], order[j-1]
		}
	}
	if delta > 0 {
		// Give spare bits to the highest-weight segments first.
		for k := 0; delta > 0; k = (k + 1) % len(order) {
			idx := order[k]
			budgets[idx]++
			delta--
		}
	} else if delta < 0 {
		// Take from the highest-budget (highest weight) segments while floors hold.
		guard := 0
		for delta < 0 {
			moved := false
			for _, idx := range order {
				if delta >= 0 {
					break
				}
				if budgets[idx] > minBits {
					budgets[idx]--
					delta++
					moved = true
				}
			}
			if !moved {
				return &AllocationError{Reason: fmt.Sprintf(
					"cannot reduce to %d bits without violating segment minimum %d", total, minBits)}
			}
			if guard++; guard > len(budgets)*2 {
				return &AllocationError{Reason: "rebalance did not converge"}
			}
		}
	}
	return nil
}

// Controller runs the bounded closed loop for one segment.
type Controller struct {
	TargetBits     int64
	DurationSec    float64
	TolerancePct   float64
	MaxIterations  int
	MinKbps        int
	MaxAdjustRatio float64 // clamp per-iteration correction, e.g. 2.0
}

// InitialKbps converts the bit budget into a starting FFmpeg target bitrate.
func (c *Controller) InitialKbps() int {
	kbps := int(math.Round(float64(c.TargetBits) / c.DurationSec / 1000.0))
	if kbps < c.MinKbps {
		kbps = c.MinKbps
	}
	return kbps
}

// NextKbps computes the corrected bitrate after an attempt produced actualBits.
// Returns the new kbps, the measured deviation percent and whether the loop is
// within tolerance and may stop.
func (c *Controller) NextKbps(prevKbps int, actualBits int64) (int, float64, bool) {
	if actualBits <= 0 {
		return c.clampKbps(prevKbps * 2), math.MaxFloat64, false
	}
	deviation := float64(actualBits-c.TargetBits) / float64(c.TargetBits) * 100.0
	if math.Abs(deviation) <= c.TolerancePct {
		return prevKbps, deviation, true
	}
	// Inverse-ratio correction: actual overshoot by 10% means bitrate down 10%.
	raw := float64(prevKbps) * float64(c.TargetBits) / float64(actualBits)
	clamped := clamp(raw, float64(prevKbps)/c.MaxAdjustRatio, float64(prevKbps)*c.MaxAdjustRatio)
	return c.clampKbps(int(math.Round(clamped))), deviation, false
}

func (c *Controller) clampKbps(kbps int) int {
	if kbps < c.MinKbps {
		return c.MinKbps
	}
	return kbps
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
