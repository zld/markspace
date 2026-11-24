// Package verify computes the quality metrics of a pipeline run: target
// vs actual bitrate deviation, sample and repair counts, manifest
// alignment error and random-access hit rate, and checks them against
// the acceptance thresholds.
package verify

import (
	"fmt"

	"mediarun/internal/clock"
	"mediarun/internal/index"
)

// Thresholds are the acceptance limits of a run.
type Thresholds struct {
	MaxBitrateDeviationPct float64 `json:"maxBitrateDeviationPct"`
	MaxAlignmentErrorMs    int64   `json:"maxAlignmentErrorMs"`
	MinHitRate             float64 `json:"minHitRate"`
}

// DefaultThresholds returns the contract thresholds: 5% bitrate
// deviation, 20 ms alignment error, 100% random-access hit rate.
func DefaultThresholds() Thresholds {
	return Thresholds{MaxBitrateDeviationPct: 5, MaxAlignmentErrorMs: 20, MinHitRate: 1.0}
}

// Metrics is the verification summary of one run.
type Metrics struct {
	TargetBitrate        int64       `json:"targetBitrate"`
	ActualBitrate        int64       `json:"actualBitrate"`
	BitrateDeviationPct  float64     `json:"bitrateDeviationPct"`
	SampleCount          int         `json:"sampleCount"`
	Duration             clock.Ticks `json:"durationTicks"`
	RepairedTimestamps   int         `json:"repairedTimestamps"`
	IndexEntries         int         `json:"indexEntries"`
	MaxAlignmentErrorMs  int64       `json:"maxAlignmentErrorMs"`
	RandomAccessQueries  int         `json:"randomAccessQueries"`
	RandomAccessHits     int         `json:"randomAccessHits"`
	QualitySummary       string      `json:"qualitySummary"`
}

// Bitrate computes bits per second for payloadBytes over dur ticks.
func Bitrate(payloadBytes int64, dur clock.Ticks) int64 {
	if dur <= 0 {
		return 0
	}
	return payloadBytes * 8 * clock.MasterTimescale / int64(dur)
}

// DeviationPct returns |actual-target|/target in percent.
func DeviationPct(actual, target int64) float64 {
	if target <= 0 {
		return 0
	}
	d := actual - target
	if d < 0 {
		d = -d
	}
	return 100 * float64(d) / float64(target)
}

// HitRate returns the random-access hit rate in [0,1].
func (m *Metrics) HitRate() float64 {
	if m.RandomAccessQueries == 0 {
		return 0
	}
	return float64(m.RandomAccessHits) / float64(m.RandomAccessQueries)
}

// FillSummary derives the quality summary string from the counters.
func (m *Metrics) FillSummary() {
	m.QualitySummary = fmt.Sprintf("samples=%d durationMs=%d repairs=%d",
		m.SampleCount, m.Duration.Millis(), m.RepairedTimestamps)
}

// Check returns the list of threshold violations (empty means pass).
func (m *Metrics) Check(th Thresholds) []string {
	var bad []string
	if m.BitrateDeviationPct > th.MaxBitrateDeviationPct {
		bad = append(bad, fmt.Sprintf("bitrate deviation %.2f%% > %.2f%%",
			m.BitrateDeviationPct, th.MaxBitrateDeviationPct))
	}
	if m.MaxAlignmentErrorMs > th.MaxAlignmentErrorMs {
		bad = append(bad, fmt.Sprintf("alignment error %dms > %dms",
			m.MaxAlignmentErrorMs, th.MaxAlignmentErrorMs))
	}
	if hr := m.HitRate(); hr < th.MinHitRate {
		bad = append(bad, fmt.Sprintf("random access hit rate %.4f < %.4f", hr, th.MinHitRate))
	}
	return bad
}

// ProbeRandomAccess issues n evenly spaced queries across the indexed
// range and counts the hits (queries answered without a boundary error).
func ProbeRandomAccess(ix *index.Index, n int) (queries, hits int) {
	if ix == nil || len(ix.Entries) == 0 || n <= 0 {
		return 0, 0
	}
	start := ix.Entries[0].Tick
	span := ix.End - start
	if span <= 0 {
		return 0, 0
	}
	for i := 0; i < n; i++ {
		t := start + clock.Ticks(int64(span)*int64(i)/int64(n))
		queries++
		if _, err := ix.Query(t); err == nil {
			hits++
		}
	}
	return queries, hits
}
