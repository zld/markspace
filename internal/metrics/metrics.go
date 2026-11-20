// Package metrics aggregates jitter-buffer counters and latency samples.
package metrics

import (
	"math"
	"sort"
)

// Sample is one periodic watermark observation.
type Sample struct {
	AtMS         int64   `json:"at_ms"`
	BufferFrames int     `json:"buffer_frames"`
	BufferMS     float64 `json:"buffer_ms"`
	TargetMS     float64 `json:"target_ms"`
	Streams      int     `json:"streams"`
}

// Summary is the complete, finalised result of a run. All fields are always
// serialised, including on empty input, so consumers can rely on the schema.
type Summary struct {
	PacketsReceived         int64 `json:"packets_received"`
	PacketsInvalid          int64 `json:"packets_invalid"`
	PacketsAccepted         int64 `json:"packets_accepted"`
	DropDuplicate           int64 `json:"drop_duplicate"`
	DropLate                int64 `json:"drop_late"`
	DropOverflow            int64 `json:"drop_overflow"`
	DropAmbiguous           int64 `json:"drop_ambiguous"`
	DropTimestampRegression int64 `json:"drop_timestamp_regression"`
	StreamLimitDrops        int64 `json:"drop_stream_limit"`
	StreamInterruptions     int64 `json:"stream_interruptions"`

	FramesPlayed   int64 `json:"frames_played"`
	HiddenFrames   int64 `json:"hidden_frames"`
	MaxHiddenRun   int64 `json:"max_hidden_run"`
	HiddenDegrades int64 `json:"hidden_degradations"`

	ContinuityRatio float64 `json:"continuity_ratio"`
	LatencyP50MS    float64 `json:"latency_p50_ms"`
	LatencyP95MS    float64 `json:"latency_p95_ms"`
	LatencySamples  int64   `json:"latency_samples"`

	BufferSamples int      `json:"buffer_samples"`
	Samples       []Sample `json:"samples"`

	latencies []float64
}

// New returns an empty summary with a non-nil sample list.
func New() *Summary {
	return &Summary{Samples: []Sample{}}
}

// AddSample appends a watermark observation.
func (s *Summary) AddSample(atMS int64, frames int, frameMS, targetMS float64, streams int) {
	s.Samples = append(s.Samples, Sample{
		AtMS:         atMS,
		BufferFrames: frames,
		BufferMS:     float64(frames) * frameMS,
		TargetMS:     round1(targetMS),
		Streams:      streams,
	})
}

// AddLatency records one end-to-end playout latency in milliseconds.
func (s *Summary) AddLatency(ms float64) {
	if ms < 0 {
		ms = 0
	}
	s.latencies = append(s.latencies, ms)
	s.LatencySamples = int64(len(s.latencies))
}

// Finalize computes percentiles, continuity and sample count. It is idempotent.
func (s *Summary) Finalize() {
	if s.Samples == nil {
		s.Samples = []Sample{}
	}
	s.BufferSamples = len(s.Samples)
	total := s.FramesPlayed + s.HiddenFrames
	if total > 0 {
		s.ContinuityRatio = round4(float64(s.FramesPlayed) / float64(total))
	} else {
		s.ContinuityRatio = 0
	}
	if len(s.latencies) > 0 {
		sort.Float64s(s.latencies)
		s.LatencyP50MS = round2(percentile(s.latencies, 0.50))
		s.LatencyP95MS = round2(percentile(s.latencies, 0.95))
	}
}

// percentile uses the nearest-rank method on sorted data.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
