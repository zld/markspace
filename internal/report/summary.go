// Package report computes the terminal-facing summary.
//
// All values are derived from artifacts the pipeline already produced, so a
// rerun against the same input reproduces the same numbers and the same
// summary hash.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"mediad/internal/fanin"
	"mediad/internal/fanout"
	"mediad/internal/merr"
	"mediad/internal/repair"
	"mediad/internal/rtp"
)

// Summary is the JSON printed by the CLI.
type Summary struct {
	// Core acceptance fields.
	SyncErrorMs        float64 `json:"sync_error_ms"`
	PacketRecoveryRate float64 `json:"packet_recovery_rate"`
	P95LatencyMs       float64 `json:"p95_latency_ms"`
	IndexEntries       int     `json:"index_entries"`
	BoundaryMismatch   int     `json:"boundary_mismatch"`
	FanInDropouts      int     `json:"fanin_dropouts"`
	MaxFanInGapMs      float64 `json:"max_fanin_gap_ms"`

	// Recovery and audit fields.
	ErrorCode             string `json:"error_code,omitempty"`
	RecoverablePrefixByte int64  `json:"recoverable_prefix_bytes"`
	RecoveryCursorEntries int    `json:"recovery_cursor_entries"`
	RecoveryCursorBlocks  int    `json:"recovery_cursor_blocks"`

	// Cross-stage invariants.
	TimestampMonotonic bool `json:"timestamps_monotonic"`
	IndexKeyframesOnly bool `json:"index_keyframes_only"`
	LayersConsistent   bool `json:"layers_consistent"`

	// Detail.
	MaxBoundaryDeviationMs float64            `json:"max_boundary_deviation_ms"`
	RecoveryRateByLayer    map[string]float64 `json:"recovery_rate_by_layer"`
	SummaryHash            string             `json:"summary_hash"`
	IndexDigest            string             `json:"index_digest"`
	MP4CrossCheckEntries   int                `json:"mp4_crosscheck_entries"`
	TrackDriftPPM          map[string]float64 `json:"track_drift_ppm,omitempty"`
	Blocks                 int                `json:"blocks"`
	Decisions              map[string]int     `json:"decisions,omitempty"`
}

// Inputs gathers the pipeline artifacts a summary is computed from.
type Inputs struct {
	Repair      *repair.Result
	Index       repair.IndexResult
	Ladder      fanout.Aggregate
	Merge       fanin.Result
	RTP         rtp.Stats
	LatenciesNs []int64
	// Damage is non-nil when the input was damaged but was still summarized.
	Damage error
	// MP4Entries is the number of entries go-mp4 read back.
	MP4Entries int
	// LayersConsistent reports that every layer stayed within its budget and
	// no layer's loss state leaked.
	LayersConsistent bool
}

// Build computes a Summary.
func Build(in Inputs) Summary {
	var s Summary
	if in.Repair != nil {
		s.SyncErrorMs = nsToMs(in.Repair.MaxAVSkewNs())
		s.Blocks = len(in.Repair.Blocks)
		s.Decisions = countDecisions(in.Repair.Decisions)
		s.TimestampMonotonic = repair.ValidateMonotonic(in.Repair.Blocks) == nil
		s.TrackDriftPPM = make(map[string]float64)
		for _, tid := range in.Repair.Timeline.Tracks() {
			if ppm, ok := in.Repair.Timeline.DriftPPM(tid); ok {
				s.TrackDriftPPM[fmt.Sprintf("track_%d", tid)] = round(ppm, 1)
			}
		}
	}
	s.IndexEntries = len(in.Index.Index.Entries)
	s.IndexDigest = in.Index.Index.Digest
	s.RecoverablePrefixByte = in.Index.Cursor.ConfirmedBytes
	s.RecoveryCursorEntries = in.Index.Cursor.Entries
	s.RecoveryCursorBlocks = in.Index.Cursor.Blocks
	s.MP4CrossCheckEntries = in.MP4Entries

	if in.Damage != nil {
		s.ErrorCode = string(codeOf(in.Damage))
	}

	s.PacketRecoveryRate = round(in.RTP.PacketRecoveryRate(), 4)
	s.RecoveryRateByLayer = make(map[string]float64, len(in.Ladder.Layers))
	for _, l := range in.Ladder.Layers {
		delivered := l.Delivered
		lost := l.Lost
		rate := 1.0
		if delivered+lost > 0 {
			rate = float64(delivered) / float64(delivered+lost)
		}
		s.RecoveryRateByLayer[l.Name] = round(rate, 4)
	}

	s.P95LatencyMs = nsToMs(percentile(in.LatenciesNs, 0.95))

	s.BoundaryMismatch = len(in.Merge.Mismatches)
	s.FanInDropouts = in.Merge.Dropouts
	s.MaxFanInGapMs = round(in.Merge.MaxDropoutMs, 3)
	s.MaxBoundaryDeviationMs = nsToMs(in.Merge.MaxBoundaryDeviationNs)
	s.LayersConsistent = in.LayersConsistent

	s.IndexKeyframesOnly = in.Repair != nil &&
		repair.EnsureKeyframesOnly(in.Index.Index, in.Repair.Blocks) == nil

	s.SummaryHash = Hash(s)
	return s
}

// Hash returns the stable digest of a summary.
//
// Only reproducible values are hashed. Publish latency is wall-clock and
// therefore varies run to run, so it is excluded; maps are serialised in
// sorted key order by encoding/json, so two runs over the same input agree on
// everything else.
func Hash(s Summary) string {
	clone := s
	clone.SummaryHash = ""
	clone.P95LatencyMs = 0
	b, err := json.Marshal(clone)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// WithHash fills SummaryHash from the current values.
func WithHash(s Summary) Summary {
	s.SummaryHash = Hash(s)
	return s
}

func codeOf(err error) string {
	return string(merr.CodeOf(err))
}

func countDecisions(ds []repair.Decision) map[string]int {
	m := make(map[string]int)
	for _, d := range ds {
		m[string(d.Kind)]++
	}
	return m
}

// percentile returns the nearest-rank percentile of ns.
func percentile(ns []int64, p float64) int64 {
	if len(ns) == 0 {
		return 0
	}
	sorted := append([]int64(nil), ns...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func nsToMs(ns int64) float64 { return round(float64(ns)/1e6, 3) }

func round(v float64, places int) float64 {
	scale := 1.0
	for i := 0; i < places; i++ {
		scale *= 10
	}
	return float64(int64(v*scale+0.5)) / scale
}

// Errors summarises invariant violations for the caller to surface.
type Errors struct {
	Monotonic  bool
	Keyframes  bool
	BoundaryOK bool
}

// Check repackages the invariant flags.
func (s Summary) Check() Errors {
	return Errors{
		Monotonic:  s.TimestampMonotonic,
		Keyframes:  s.IndexKeyframesOnly,
		BoundaryOK: s.BoundaryMismatch == 0,
	}
}

// Tracker computes a latency percentile over a bounded sample set.
type Tracker struct {
	samples []int64
	limit   int
	next    int
}

// NewTracker returns a latency tracker retaining up to limit samples.
func NewTracker(limit int) *Tracker {
	if limit <= 0 {
		limit = 256
	}
	return &Tracker{limit: limit, samples: make([]int64, 0, limit)}
}

// Add records one latency sample in nanoseconds.
func (t *Tracker) Add(ns int64) {
	if len(t.samples) < t.limit {
		t.samples = append(t.samples, ns)
		return
	}
	t.samples[t.next%t.limit] = ns
	t.next++
}

// Samples returns the retained samples.
func (t *Tracker) Samples() []int64 { return t.samples }

// P95 returns the 95th percentile in nanoseconds.
func (t *Tracker) P95() int64 { return percentile(t.samples, 0.95) }
