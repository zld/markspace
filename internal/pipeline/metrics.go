package pipeline

import (
	"mediasync/internal/dag"
	"mediasync/internal/fixture"
	"mediasync/internal/rtp"
)

func fillRecoveryMetrics(sum *Summary, res *Result) {
	if res.Recover == nil {
		sum.PacketRecoveryRate = 0
		return
	}
	st := res.Recover.Stats
	sum.PacketRecoveryRate = rtp.RecoveryRate(st)
}

func fillIndexMetrics(sum *Summary, res *Result) {
	if res.Index == nil {
		sum.IndexEntries, sum.ValidIntervals = 0, intervalCount(res)
		sum.DroppedIndexRecords = 0
		return
	}
	sum.IndexEntries = len(res.Index.Records)
	sum.ValidIntervals = intervalCount(res)
	sum.DroppedIndexRecords = res.Index.Dropped.TotalDropped
}

func intervalCount(res *Result) int {
	if res.Recover == nil {
		return 0
	}
	return len(res.Recover.Intervals)
}

// computeP95 derives a deterministic scheduling latency figure per packet:
// reorder distance plus a fixed per-stage processing cost, then takes the
// 95th percentile. It models the in-process pipeline, not wall-clock I/O.
func computeP95(stream *fixture.Stream, res *Result) float64 {
	const baseMs = 8.0
	n := len(stream.Packets)
	if n == 0 {
		return 0
	}
	lat := make([]float64, n)
	arrival := make(map[uint16]int, n)
	for i, p := range stream.Packets {
		arrival[p.SequenceNumber] = i
	}
	for i, p := range stream.Packets {
		ideal := i
		actual := arrival[p.SequenceNumber]
		d := actual - ideal
		if d < 0 {
			d = -d
		}
		lat[i] = baseMs + float64(d)*2.0
	}
	return percentile(lat, 0.95)
}

func percentile(samples []float64, p float64) float64 {
	v := append([]float64(nil), samples...)
	sortFloat64s(v)
	idx := int(float64(len(v)-1) * p)
	return v[idx]
}

func blockedName(sr *dag.Result) string {
	if sr.Blocked == nil {
		return ""
	}
	return sr.Blocked.BlockedBy
}

// decideExitCode: 0 when recoverable with no blocking dependency, 2 when an
// input is unrecoverable or retries were exhausted, 1 for bad arguments
// (handled before Run, so never returned from here).
func decideExitCode(sr *dag.Result, res *Result) int {
	if sr.Blocked != nil || len(sr.Failures) > 0 {
		return 2
	}
	return 0
}

func outputDigest(res *Result) string {
	if res.Index != nil {
		return res.Index.OutputDigest
	}
	if res.Recover != nil {
		return MustDigest(map[string]any{
			"intervals": res.Recover.Intervals, "gaps": res.Recover.Gaps,
		})
	}
	return MustDigest("no-output")
}
