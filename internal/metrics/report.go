// Package metrics accumulates receive-path counters and renders JSON and text
// reports: buffer watermark curve, classified packet drops, concealment
// continuity and playout latency quantiles.
package metrics

import (
	"sort"
	"time"
)

// DropCounters holds one independent counter per discard reason.
type DropCounters struct {
	Corrupt    uint64 `json:"corrupt_packets"`
	BadVersion uint64 `json:"bad_version_packets"`
	BadLength  uint64 `json:"bad_length_packets"`
	Ambiguous  uint64 `json:"ambiguous_packets"`
	Duplicate  uint64 `json:"duplicate_drops"`
	Late       uint64 `json:"late_drops"`
	Overflow   uint64 `json:"overflow_drops"`
	Reordered  uint64 `json:"reordered_packets"`
}

// Invalid sums all packets rejected before entering the reorder window.
func (d DropCounters) Invalid() uint64 {
	return d.Corrupt + d.BadVersion + d.BadLength + d.Ambiguous
}

// LevelPoint is one sampled point of the buffer-level curve.
type LevelPoint struct {
	T     time.Duration `json:"t_ms"`
	Level int           `json:"level_frames"`
}

// Report is the full serialized result of one run.
type Report struct {
	Frames           uint64       `json:"frames"`
	Received         uint64       `json:"received"`
	Played           uint64       `json:"played"`
	ConcealedFrames  uint64       `json:"concealed_frames"`
	MaxConcealRun    int          `json:"max_conceal_run"`
	MaxRecoveryJump  int          `json:"max_recovery_jump"`
	RecoveryRatio    float64      `json:"recovery_ratio"`
	LatencyP50US     int64        `json:"latency_p50_us"`
	LatencyP95US     int64        `json:"latency_p95_us"`
	LatencyP99US     int64        `json:"latency_p99_us"`
	LatencyMaxUS     int64        `json:"latency_max_us"`
	BufferLevelCurve []LevelPoint `json:"buffer_level_curve"`
	InvalidPackets   uint64       `json:"invalid_packets"`
	DropCounters
}

// maxCurvePoints keeps JSON output in the hundreds-to-thousands range.
const maxCurvePoints = 2048

// Collector gathers samples during a run; zero value is not usable, call New.
type Collector struct {
	rep      Report
	levels   []LevelPoint
	latency  []time.Duration
	stride   int
	seen     int
	episodes uint64
	smooth   uint64
}

// New prepares a collector for an expected frame count.
func New(expectedFrames uint64) *Collector {
	c := &Collector{}
	c.rep.BufferLevelCurve = []LevelPoint{}
	c.stride = 1
	if expectedFrames > maxCurvePoints {
		c.stride = int(expectedFrames/maxCurvePoints) + 1
	}
	return c
}

// Drops exposes the mutable drop counters for the receive path.
func (c *Collector) Drops() *DropCounters { return &c.rep.DropCounters }

// AddReceived counts one successfully decoded, valid packet.
func (c *Collector) AddReceived() { c.rep.Received++ }

// RecordFrame logs one played frame with the current buffer depth.
func (c *Collector) RecordFrame(at time.Duration, levelFrames int,
	latency time.Duration, concealed bool) {
	c.rep.Played++
	if concealed {
		c.rep.ConcealedFrames++
	}
	if c.seen%c.stride == 0 {
		c.levels = append(c.levels, LevelPoint{T: at, Level: levelFrames})
	}
	c.seen++
	if latency >= 0 {
		c.latency = append(c.latency, latency)
	}
}

// SetConcealment copies concealment summary counters into the report.
func (c *Collector) SetConcealment(maxRun, maxJump int,
	episodes, smooth uint64) {
	c.rep.MaxConcealRun = maxRun
	c.rep.MaxRecoveryJump = maxJump
	c.episodes = episodes
	c.smooth = smooth
}

// SetFrames records the total simulated frame count.
func (c *Collector) SetFrames(n uint64) { c.rep.Frames = n }

// AddPendingGaps is used at end-of-input to account for unresolved gaps.
func (c *Collector) AddPendingGaps(n int) {
	c.rep.ConcealedFrames += uint64(n)
	if n > c.rep.MaxConcealRun {
		c.rep.MaxConcealRun = n
	}
}

// Build finalizes quantiles, the curve and derived ratios.
func (c *Collector) Build() Report {
	c.rep.BufferLevelCurve = c.levels
	c.rep.InvalidPackets = c.rep.DropCounters.Invalid()
	if c.episodes > 0 {
		c.rep.RecoveryRatio = float64(c.smooth) / float64(c.episodes)
	}
	c.rep.LatencyP50US = c.quantile(0.50).Microseconds()
	c.rep.LatencyP95US = c.quantile(0.95).Microseconds()
	c.rep.LatencyP99US = c.quantile(0.99).Microseconds()
	c.rep.LatencyMaxUS = c.quantile(1.00).Microseconds()
	return c.rep
}

func (c *Collector) quantile(q float64) time.Duration {
	if len(c.latency) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), c.latency...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
}
