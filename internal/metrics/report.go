// Package metrics quantifies synchronization, recovery, latency, and
// deterministic pipeline status.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"time"
)

// Report contains the acceptance metrics.
type Report struct {
	AVSyncMS               float64 `json:"av_sync_ms"`
	LossRecoveryRate       float64 `json:"loss_recovery_rate"`
	P95LatencyMS           float64 `json:"p95_latency_ms"`
	ManifestSegments       int     `json:"manifest_segments"`
	AlignmentErrors        int     `json:"alignment_errors"`
	FirstBlockedDependency string  `json:"first_blocked_dependency"`
	ManifestSHA256         string  `json:"manifest_sha256"`

	// Supporting counters are printed to make the seven required values
	// independently auditable.
	RecoveredPackets int   `json:"recovered_packets,omitempty"`
	UnrecoveredGaps  int   `json:"unrecovered_gaps,omitempty"`
	GeneratedPackets int   `json:"generated_packets,omitempty"`
	IndexRebuilt     int   `json:"index_rebuilt,omitempty"`
	TruncationOffset int64 `json:"truncation_offset,omitempty"`
	FirstUnreadable  int64 `json:"first_unreadable_offset,omitempty"`
	LatencySamples   int   `json:"latency_samples,omitempty"`
	ExitCode         int   `json:"-"`
}

// P95Millis computes nearest-rank P95 from local sample processing durations.
// It must only receive timings from self-generated local samples.
func P95Millis(samples []time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return float64(sorted[rank].Nanoseconds()) / 1e6
}

// PrintKeyValues writes stable key=value terminal metrics.
func (r Report) PrintKeyValues(w io.Writer) {
	if r.FirstBlockedDependency == "" {
		r.FirstBlockedDependency = "none"
	}
	fmt.Fprintf(w, "av_sync_ms=%.3f\n", r.AVSyncMS)
	fmt.Fprintf(w, "loss_recovery_rate=%.6f\n", r.LossRecoveryRate)
	fmt.Fprintf(w, "p95_latency_ms=%.3f\n", r.P95LatencyMS)
	fmt.Fprintf(w, "manifest_segments=%d\n", r.ManifestSegments)
	fmt.Fprintf(w, "alignment_errors=%d\n", r.AlignmentErrors)
	fmt.Fprintf(w, "first_blocked_dependency=%s\n", r.FirstBlockedDependency)
	fmt.Fprintf(w, "manifest_sha256=%s\n", r.ManifestSHA256)

	// Audit fields.
	fmt.Fprintf(w, "recovered_packets=%d\n", r.RecoveredPackets)
	fmt.Fprintf(w, "unrecovered_gaps=%d\n", r.UnrecoveredGaps)
	fmt.Fprintf(w, "generated_packets=%d\n", r.GeneratedPackets)
	fmt.Fprintf(w, "index_rebuilt=%d\n", r.IndexRebuilt)
	fmt.Fprintf(w, "latency_samples=%d\n", r.LatencySamples)
	if r.TruncationOffset > 0 || r.FirstUnreadable > 0 {
		fmt.Fprintf(w, "truncation_offset=%d\n", r.TruncationOffset)
		fmt.Fprintf(w, "first_unreadable_offset=%d\n", r.FirstUnreadable)
	}
	fmt.Fprintf(w, "exit_code=%d\n", r.ExitCode)
}
