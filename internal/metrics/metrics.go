// Package metrics computes the quantitative evidence emitted by the QC run:
// latency percentiles, recovery rates, sync error and gap counters. All
// computations are deterministic and allocation-bounded.
package metrics

import (
	"math"
	"sort"
)

// Percentile returns the p-th percentile (0 < p <= 100) using nearest-rank on
// a copied, sorted slice; the input is never mutated.
func Percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 || p <= 0 || p > 100 {
		return 0
	}
	cp := append([]float64(nil), samples...)
	sort.Float64s(cp)
	rank := math.Ceil(p / 100.0 * float64(len(cp)))
	idx := int(rank) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

// P95 is a convenience wrapper for the 95th percentile.
func P95(samples []float64) float64 { return Percentile(samples, 95) }

// RecoveryRate returns recovered/lost; zero loss yields zero.
func RecoveryRate(recovered, lost int) float64 {
	if lost <= 0 {
		return 0
	}
	return float64(recovered) / float64(lost)
}

// MaxAbs returns the largest absolute value, or zero when empty.
func MaxAbs(values []float64) float64 {
	m := 0.0
	for _, v := range values {
		if v < 0 {
			v = -v
		}
		if v > m {
			m = v
		}
	}
	return m
}

// Summary aggregates the scalar evidence for one run.
type Summary struct {
	SampleCount          int     `json:"sample_count"`
	P95LatencyMs         float64 `json:"p95_latency_ms"`
	AVSyncErrorMs        float64 `json:"av_sync_error_ms"`
	PacketLossRecovery   float64 `json:"packet_loss_recovery_rate"`
	UnrecoverableGaps    int     `json:"unrecoverable_gaps"`
	InvariantFailures    int     `json:"invariant_failures"`
	RecoveredPackets     int     `json:"recovered_packets"`
	LostPackets          int     `json:"lost_packets"`
}
