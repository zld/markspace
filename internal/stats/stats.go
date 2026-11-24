// Package stats computes bitrate and quality statistics for a remux task.
// All computations are deterministic functions of the recorded counters so
// repeated runs over the same input yield identical summaries.
package stats

import (
	"fmt"

	"mediacheck/internal/rtp"
)

// Bitrate compares the target bitrate with the achieved one.
type Bitrate struct {
	TargetKbps float64
	ActualKbps float64
	Delta      float64 // relative deviation |actual-target|/target
}

// ComputeBitrate derives the actual bitrate from written bytes and media
// duration and compares it against the target.
func ComputeBitrate(bytesWritten int64, durationNanos int64, targetKbps float64) Bitrate {
	b := Bitrate{TargetKbps: targetKbps}
	if durationNanos > 0 {
		b.ActualKbps = float64(bytesWritten) * 8 * 1e9 / float64(durationNanos) / 1000
	}
	if targetKbps > 0 {
		d := b.ActualKbps - targetKbps
		if d < 0 {
			d = -d
		}
		b.Delta = d / targetKbps
	}
	return b
}

// Within reports whether the relative deviation is inside maxDelta.
func (b Bitrate) Within(maxDelta float64) bool { return b.Delta <= maxDelta }

func (b Bitrate) String() string {
	return fmt.Sprintf("target=%.0fkbps actual=%.1fkbps delta=%.2f%%",
		b.TargetKbps, b.ActualKbps, b.Delta*100)
}

// Quality aggregates the per-task quality counters.
type Quality struct {
	RTP             rtp.Stats
	Ranges          []rtp.Range
	SamplesWritten  int64
	BytesWritten    int64
	DurationNanos   int64
	DesyncSegments  int
	ManifestEntries int
}

// UnrecoveredRanges returns the ranges that could not be recovered.
func (q Quality) UnrecoveredRanges() []rtp.Range {
	var out []rtp.Range
	for _, r := range q.Ranges {
		if !r.Recoverable {
			out = append(out, r)
		}
	}
	return out
}
