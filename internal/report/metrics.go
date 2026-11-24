// Package report assembles the deterministic, machine-readable summary
// consumed by the terminal adapter. Every metric is derived from upstream
// subsystem outputs; this package performs no independent analysis, so the
// summary is reproducible bit-for-bit from identical inputs.
package report

import (
	"sort"

	"mediacheck/internal/bitstream"
	"mediacheck/internal/damage"
	"mediacheck/internal/media"
	rtppkg "mediacheck/internal/rtp"
	"mediacheck/internal/sync"
)

// TrackSummary is one track's structural and cadence summary.
type TrackSummary struct {
	TrackID     uint32 `json:"track_id"`
	Kind        string `json:"kind"`
	Timescale   uint32 `json:"timescale"`
	SampleCount int    `json:"sample_count"`
	Undecidable bool   `json:"undecidable"`
	Monotonic   bool   `json:"monotonic"`
	Keyframes   int    `json:"keyframes"`
}

// Metrics is the full quantitative report matching the CLI output contract.
type Metrics struct {
	Tracks []TrackSummary `json:"tracks"`
	// KeyframeGapMs is the per-track representative (p95) keyframe gap.
	KeyframeGapMs map[uint32]int64 `json:"keyframe_gap_ms"`
	DamageRanges  []damage.Range   `json:"damage_ranges"`
	DamageErrors  []damage.Error   `json:"damage_errors"`
	// RecoveredPackets and LossRecoveryRate come from RTP recovery.
	RecoveredPackets int     `json:"recovered_packets"`
	LossRecoveryRate float64 `json:"loss_recovery_rate"`
	// AVSyncErrorMs is the absolute-mean A/V offset; 0 when no pair exists.
	AVSyncErrorMs float64 `json:"av_sync_error_ms"`
	// ClockDriftPPM is the fitted RTP remote-clock skew in parts per
	// million; 0 when no RTP stream was analyzed.
	ClockDriftPPM float64 `json:"clock_drift_ppm"`
	// P95LatencyMs is the end-to-end 95th-percentile processing latency.
	P95LatencyMs int64 `json:"p95_latency_ms"`
	// HasUnrecoverable drives CLI exit code 3.
	HasUnrecoverable bool `json:"has_unrecoverable"`
}

// Input bundles every upstream result the summary is built from.
type Input struct {
	Lines     []*media.TrackLine
	Keyframes []bitstream.Report
	Damages   []damage.Error
	RTP       *rtppkg.Result
	Sync      *sync.Report
	Latencies []int64 // per-stage processing latencies, nanoseconds
}

// Build constructs the metrics deterministically. Map keys and slice
// ordering are normalized so two identical inputs serialize identically.
func Build(in Input) Metrics {
	m := Metrics{
		KeyframeGapMs: map[uint32]int64{},
		DamageErrors:  append([]damage.Error(nil), in.Damages...),
	}
	sort.SliceStable(m.DamageErrors, func(i, j int) bool {
		if m.DamageErrors[i].TrackID != m.DamageErrors[j].TrackID {
			return m.DamageErrors[i].TrackID < m.DamageErrors[j].TrackID
		}
		if m.DamageErrors[i].Code != m.DamageErrors[j].Code {
			return m.DamageErrors[i].Code < m.DamageErrors[j].Code
		}
		return m.DamageErrors[i].Range.StartNanos < m.DamageErrors[j].Range.StartNanos
	})
	for _, e := range m.DamageErrors {
		m.DamageRanges = append(m.DamageRanges, e.Range)
		if e.Recoverable == damage.RecoverableNo {
			m.HasUnrecoverable = true
		}
	}

	lineByID := map[uint32]*media.TrackLine{}
	for _, l := range in.Lines {
		lineByID[l.TrackID] = l
		m.Tracks = append(m.Tracks, TrackSummary{
			TrackID:     l.TrackID,
			Kind:        l.Kind.String(),
			Timescale:   l.TimeBase.TicksPerSecond(),
			SampleCount: len(l.Points),
			Undecidable: l.Undecidable,
			Monotonic:   l.Monotonic,
		})
	}
	sort.Slice(m.Tracks, func(i, j int) bool { return m.Tracks[i].TrackID < m.Tracks[j].TrackID })

	for i := range in.Keyframes {
		k := &in.Keyframes[i]
		m.KeyframeGapMs[k.Stats.TrackID] = k.Stats.P95Ms
		if ts, ok := m.trackSummary(k.Stats.TrackID); ok {
			ts.Keyframes = k.Stats.KeyframeCount
		}
	}

	if in.RTP != nil {
		m.RecoveredPackets = in.RTP.Stats.Recovered
		m.LossRecoveryRate = in.RTP.Stats.LossRecoveryRate
		m.ClockDriftPPM = in.RTP.Drift.PPM
	}
	if in.Sync != nil {
		m.AVSyncErrorMs = in.Sync.AbsMeanMs
	}
	m.P95LatencyMs = percentile95Millis(in.Latencies)
	return m
}

func (m *Metrics) trackSummary(id uint32) (*TrackSummary, bool) {
	for i := range m.Tracks {
		if m.Tracks[i].TrackID == id {
			return &m.Tracks[i], true
		}
	}
	return nil, false
}

// percentile95Millis converts nanosecond latency samples to a nearest-rank
// 95th percentile in milliseconds. Empty input yields zero.
func percentile95Millis(samplesNanos []int64) int64 {
	if len(samplesNanos) == 0 {
		return 0
	}
	s := append([]int64(nil), samplesNanos...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := (95*len(s) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return s[rank-1] / 1_000_000
}
