// Package metrics turns the deterministic record/point streams produced by
// the other subsystems into locally verifiable numerical evidence. Nothing
// here decodes payloads or claims subjective quality: every statistic is a
// function of sample sizes, drop counts and integer timestamps.
package metrics

import (
	"sort"

	"mediacore/internal/clock"
)

// Quality is a fully reproducible description of sample-size behavior.
type Quality struct {
	SampleCount int
	MinBytes    uint32
	MaxBytes    uint32
	MeanBytes   uint64 // rounded integer mean
	// P50/P90/P99 are computed from a sorted copy of the size distribution;
	// the input slice is never mutated.
	P50 uint32
	P90 uint32
	P99 uint32
	// StdDev is the population standard deviation in bytes, rounded.
	StdDev uint64
}

// ComputeQuality builds the size distribution statistics. The implementation
// makes one sorted copy, so memory is O(n) but n is the batch-level window in
// production callers, never an unbounded accumulation.
func ComputeQuality(wireSizes []uint32) (Quality, error) {
	n := len(wireSizes)
	if n == 0 {
		return Quality{}, clock.ErrInvalidArgument("metrics: empty quality sample set")
	}
	sorted := append([]uint32(nil), wireSizes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum uint64
	min, max := sorted[0], sorted[n-1]
	for _, v := range sorted {
		sum += uint64(v)
	}
	mean := (sum + uint64(n)/2) / uint64(n)

	var sqAcc uint64
	for _, v := range wireSizes {
		d := int64(v) - int64(mean)
		sqAcc += uint64(d * d)
	}
	variance := sqAcc / uint64(n)
	return Quality{
		SampleCount: n,
		MinBytes:    min,
		MaxBytes:    max,
		MeanBytes:   mean,
		P50:         percentile(sorted, 50),
		P90:         percentile(sorted, 90),
		P99:         percentile(sorted, 99),
		StdDev:      isqrt(variance),
	}, nil
}

// percentile returns the nearest-rank value (ceil(p/100 * n), 1-based).
func percentile(sorted []uint32, p int) uint32 {
	n := len(sorted)
	rank := (p*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// isqrt is an integer square root so variance reporting never introduces a
// floating-point dependency into the report.
func isqrt(x uint64) uint64 {
	if x == 0 {
		return 0
	}
	var r uint64 = 1
	for r*r <= x {
		r <<= 1
		if r == 0 {
			return x
		}
	}
	var lo, hi = r >> 1, r
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if mid*mid <= x {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}
