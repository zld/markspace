// Package report aggregates the deterministic per-subsystem results into one
// JSON-ready summary. All percentile and rate math lives here so the demo
// command stays a thin adapter.
package report

import (
	"sort"
)

// LatencySample is one observed processing latency with its event timestamp.
type LatencySample struct {
	EventTimeNS int64
	LatencyNS   int64
}

// SyncSample pairs an audio and a video point on the shared clock.
type SyncSample struct {
	AudioNS int64
	VideoNS int64
}

// Builder accumulates measurements and freezes them into a Summary.
type Builder struct {
	latencies []LatencySample
	sync      []SyncSample
}

// NewBuilder returns an empty aggregator.
func NewBuilder() *Builder { return &Builder{} }

// RecordLatency adds one latency observation.
func (b *Builder) RecordLatency(eventTimeNS, latencyNS int64) {
	if latencyNS < 0 {
		latencyNS = 0
	}
	b.latencies = append(b.latencies, LatencySample{EventTimeNS: eventTimeNS, LatencyNS: latencyNS})
}

// RecordSync adds one audio/video alignment observation on the shared clock.
func (b *Builder) RecordSync(audioNS, videoNS int64) {
	b.sync = append(b.sync, SyncSample{AudioNS: audioNS, VideoNS: videoNS})
}

// Percentile computes the P-pct value (0..100) with the nearest-rank method,
// which needs no interpolation and is deterministic for a fixed input.
func Percentile(values []int64, pct float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if pct <= 0 {
		return sorted[0]
	}
	if pct >= 100 {
		return sorted[len(sorted)-1]
	}
	// Nearest rank: ceil(pct/100 * N), 1-based index.
	rank := int((pct/100.0)*float64(len(sorted)) + 0.9999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// absInt64 returns the absolute value without MinInt64 overflow surprises for
// the timestamp magnitudes this kernel handles.
func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// SyncErrorMS returns the maximum audio/video mismatch across observations,
// in milliseconds, rounded half away from zero. Zero observations -> 0.
func (b *Builder) SyncErrorMS() float64 {
	if len(b.sync) == 0 {
		return 0
	}
	var worst int64
	for _, s := range b.sync {
		if d := absInt64(s.AudioNS - s.VideoNS); d > worst {
			worst = d
		}
	}
	return round2(float64(worst) / 1e6)
}

// P95LatencyMS returns the nearest-rank P95 latency in milliseconds.
func (b *Builder) P95LatencyMS() float64 {
	if len(b.latencies) == 0 {
		return 0
	}
	vals := make([]int64, len(b.latencies))
	for i, l := range b.latencies {
		vals[i] = l.LatencyNS
	}
	return round2(float64(Percentile(vals, 95)) / 1e6)
}

// LatencyCount reports how many latency samples were recorded.
func (b *Builder) LatencyCount() int { return len(b.latencies) }

// Summary is the final JSON contract printed by the demo command. Field names
// are part of the acceptance contract and must not change casually.
type Summary struct {
	SyncErrorMS        float64  `json:"sync_error_ms"`
	PacketRecoveryRate float64  `json:"packet_recovery_rate"`
	P95LatencyMS       float64  `json:"p95_latency_ms"`
	BufferPeakBytes    int64    `json:"buffer_peak_bytes"`
	LateRecomputed     int      `json:"late_recomputed"`
	LateRejected       int      `json:"late_rejected"`
	MetadataConflicts  int      `json:"metadata_conflicts"`
	ErrorCodes         []string `json:"error_codes"`
	TimelineHash       uint64   `json:"timeline_hash"`
}

// Assemble freezes every subsystem result into the acceptance Summary.
func Assemble(p Params) Summary {
	codes := append([]string(nil), p.ErrorCodes...)
	sort.Strings(codes)
	codes = dedupe(codes)
	return Summary{
		SyncErrorMS:        p.Builder.SyncErrorMS(),
		PacketRecoveryRate: round4(p.RecoveryRate),
		P95LatencyMS:       p.Builder.P95LatencyMS(),
		BufferPeakBytes:    p.BufferPeakBytes,
		LateRecomputed:     p.LateRecomputed,
		LateRejected:       p.LateRejected,
		MetadataConflicts:  p.MetadataConflicts,
		ErrorCodes:         codes,
		TimelineHash:       p.TimelineHash,
	}
}

// Params bundles subsystem outputs for Assemble.
type Params struct {
	Builder           *Builder
	RecoveryRate      float64
	BufferPeakBytes   int64
	LateRecomputed    int
	LateRejected      int
	MetadataConflicts int
	ErrorCodes        []string
	TimelineHash      uint64
}

func round2(v float64) float64 { return roundTo(v, 100) }
func round4(v float64) float64 { return roundTo(v, 10000) }

func roundTo(v, scale float64) float64 {
	if v < 0 {
		return -roundTo(-v, scale)
	}
	return float64(int64(v*scale+0.5)) / scale
}

func dedupe(sorted []string) []string {
	if len(sorted) <= 1 {
		return sorted
	}
	out := sorted[:1]
	for _, s := range sorted[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
