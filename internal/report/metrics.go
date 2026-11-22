// Package report independently reconciles the facts produced by every stage
// — input demux summary, output remux outcome, recovery and pipeline
// statistics — into one quantitative summary. It performs no processing of
// its own; it only recomputes and cross-checks, which is what lets it catch a
// stage that silently dropped or altered samples.
package report

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// InputSummary is the demuxer's independently verifiable view of the source.
type InputSummary struct {
	TimecodeScaleNS int64
	TrackBlocks     map[uint32]int // blocks per source track number
	TotalBlocks     int
	InputBytes      int64
	Truncated       bool
	TruncOffset     int64
}

// OutputSummary is the writer's view of the published package.
type OutputSummary struct {
	TracksMapped int
	Samples      int
	OutputBytes  int64
	Clusters     int
	PayloadHash  [32]byte
	Published    bool
}

// RecoverySummary carries the RTP recovery accounting.
type RecoverySummary struct {
	Expected      int
	Received      int
	Duplicates    int
	Reordered     int
	Recovered     int
	Unrecoverable int
}

// RecoveryRate recomputes the delivery fraction rather than trusting a
// caller-supplied value.
func (r RecoverySummary) RecoveryRate() float64 {
	if r.Expected == 0 {
		return 1
	}
	return float64(r.Expected-r.Unrecoverable) / float64(r.Expected)
}

// Metrics is the final quantitative, machine-checkable summary.
type Metrics struct {
	SyncErrorMSP95       float64 `json:"sync_error_ms_p95"`
	PacketRecoveryRate   float64 `json:"packet_recovery_rate"`
	PipelineLatencyMSP95 float64 `json:"pipeline_latency_ms_p95"`
	TracksMapped         int     `json:"tracks_mapped"`
	DamagedCases         int     `json:"damaged_cases"`
	RerunDigestEqual     bool    `json:"rerun_digest_equal"`
	RecoverableCases     int     `json:"recoverable_cases"`
	RejectedWrites       int     `json:"rejected_writes"`
	InputBlocks          int     `json:"input_blocks"`
	OutputSamples        int     `json:"output_samples"`
}

// ErrInvariant is returned when independently computed summaries disagree, so
// the command layer can distinguish an internal consistency failure from a
// damaged input.
var ErrInvariant = errors.New("report: invariant violation")

// Percentile returns the p-th percentile (0..100) using nearest-rank on a
// sorted copy, so the caller's slice is not mutated.
func Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p / 100.0 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Options carries the raw measurements the reconciliation needs.
type Options struct {
	Input          InputSummary
	Output         OutputSummary
	Recovery       RecoverySummary
	SyncErrorsMS   []float64 // absolute sync error per merged sample
	LatenciesMS    []float64 // per-task pipeline latency
	FirstDigest    [32]byte
	SecondDigest   [32]byte
	DamagedCases   int
	Recoverable    int
	RejectedWrites int
}

// Build recomputes every metric and cross-checks the stage summaries. It
// returns ErrInvariant (wrapped) when input and output disagree in ways no
// stage is allowed to explain away.
func Build(o Options) (Metrics, error) {
	m := Metrics{
		SyncErrorMSP95:       round2(Percentile(o.SyncErrorsMS, 95)),
		PacketRecoveryRate:   round4(o.Recovery.RecoveryRate()),
		PipelineLatencyMSP95: round2(Percentile(o.LatenciesMS, 95)),
		TracksMapped:         o.Output.TracksMapped,
		DamagedCases:         o.DamagedCases,
		// Zero-value digests mean no output was measured, so an all-zero
		// coincidence must not be reported as a confirmed rerun equality.
		RerunDigestEqual: o.FirstDigest != [32]byte{} && o.FirstDigest == o.SecondDigest,
		RecoverableCases: o.Recoverable,
		RejectedWrites:   o.RejectedWrites,
		InputBlocks:      o.Input.TotalBlocks,
		OutputSamples:    o.Output.Samples,
	}
	if err := reconcile(o, m); err != nil {
		return m, err
	}
	return m, nil
}

// reconcile enforces the cross-stage invariants.
func reconcile(o Options, m Metrics) error {
	// 1. A published package must carry exactly the samples that were mapped.
	if o.Output.Published && o.Output.Samples != o.Input.TotalBlocks {
		return fmt.Errorf("%w: output samples %d != input blocks %d",
			ErrInvariant, o.Output.Samples, o.Input.TotalBlocks)
	}
	// 2. Per-track block counts must sum to the declared total.
	sum := 0
	for _, n := range o.Input.TrackBlocks {
		if n < 0 {
			return fmt.Errorf("%w: negative track block count", ErrInvariant)
		}
		sum += n
	}
	if sum != o.Input.TotalBlocks {
		return fmt.Errorf("%w: per-track blocks %d != total %d",
			ErrInvariant, sum, o.Input.TotalBlocks)
	}
	// 3. Recovery accounting must be internally consistent.
	rcv := o.Recovery
	if rcv.Received+rcv.Unrecoverable > rcv.Expected+rcv.Duplicates {
		return fmt.Errorf("%w: recovery counts exceed observed span", ErrInvariant)
	}
	if m.PacketRecoveryRate < 0 || m.PacketRecoveryRate > 1 {
		return fmt.Errorf("%w: recovery rate out of [0,1]: %f",
			ErrInvariant, m.PacketRecoveryRate)
	}
	// 4. A claimed rerun-equal digest requires an actual published output.
	if !o.Output.Published && m.RerunDigestEqual {
		return fmt.Errorf("%w: digest equal claimed without published output", ErrInvariant)
	}
	return nil
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }
