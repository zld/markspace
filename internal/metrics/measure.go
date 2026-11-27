// Package metrics turns reconstructed packets and alignment reports into
// quantifiable latency, loss, recovery and synchronization statistics.
package metrics

import (
	"sort"

	"mediasync/internal/model"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
)

// LatencyStats summarizes end-to-end packet latency on the shared timeline.
type LatencyStats struct {
	Count       int     `json:"count"`
	MinNS       model.Ticks `json:"min_ns"`
	MaxNS       model.Ticks `json:"max_ns"`
	MeanNS      model.Ticks `json:"mean_ns"`
	P95NS       model.Ticks `json:"p95_ns"`
	P95Millis   float64 `json:"p95_ms"`
}

// LossStats quantifies loss, duplication and window-based recovery.
type LossStats struct {
	InjectedLosses int     `json:"injected_losses"`
	Missing        int     `json:"missing"`
	Recovered      int     `json:"recovered"`
	Unrecovered    int     `json:"unrecovered_outside_window"`
	Duplicates     int     `json:"duplicates"`
	Late           int     `json:"late"`
	RecoveryRate   float64 `json:"loss_recovery_rate"`
}

// SyncStats exposes A/V synchronization error in nanoseconds and the
// acceptance threshold used by callers.
type SyncStats struct {
	Matched     int     `json:"matched"`
	Missing     int     `json:"missing"`
	MaxErrorNS  model.Ticks `json:"max_sync_error_ns"`
	P95ErrorNS  model.Ticks `json:"p95_sync_error_ns"`
	ThresholdNS model.Ticks `json:"threshold_ns"`
	WithinLimit bool    `json:"within_limit"`
}

// MeasureLatency computes latency = arrival time minus the RTP clock time
// mapped to nanoseconds. The first packet establishes the clock offset, so
// absolute wall-clock origins do not matter.
func MeasureLatency(committed []rtp.Packet) LatencyStats {
	if len(committed) == 0 {
		return LatencyStats{}
	}
	latencies := make([]model.Ticks, len(committed))
	var sum model.Ticks
	minV, maxV := committed[0].ArrivalNS, model.Ticks(0)
	for i, p := range committed {
		lat := p.ArrivalNS - p.ScheduleNS
		latencies[i] = lat
		sum += lat
		if lat < minV {
			minV = lat
		}
		if lat > maxV {
			maxV = lat
		}
	}
	mean := sum / model.Ticks(len(committed))
	p95 := percentile(latencies, 95)
	return LatencyStats{
		Count:     len(committed),
		MinNS:     minV,
		MaxNS:     maxV,
		MeanNS:    mean,
		P95NS:     p95,
		P95Millis: p95.Millis(),
	}
}

// MeasureLoss reconciles the known injected loss count with reconstruction
// counters. Recovered packets are those that arrived inside the reorder
// window; unrecovered ones were declared missing after the window expired.
func MeasureLoss(st rtp.Stats, injected int) LossStats {
	recovered := 0
	if injected > st.Missing {
		recovered = injected - st.Missing
	}
	rate := 1.0
	if injected > 0 {
		rate = float64(recovered) / float64(injected)
	}
	return LossStats{
		InjectedLosses: injected,
		Missing:        st.Missing,
		Recovered:      recovered,
		Unrecovered:    st.Missing,
		Duplicates:     st.Duplicates,
		Late:           st.Late,
		RecoveryRate:   rate,
	}
}

// MeasureSync projects an alignment report into sync statistics and records
// whether the maximum absolute error stays within threshold.
func MeasureSync(rep *sync.Report, thresholdNS model.Ticks) SyncStats {
	if rep == nil {
		return SyncStats{ThresholdNS: thresholdNS}
	}
	return SyncStats{
		Matched:     rep.Matched,
		Missing:     rep.Missing,
		MaxErrorNS:  rep.MaxAbsErrNS,
		P95ErrorNS:  rep.P95ErrNS,
		ThresholdNS: thresholdNS,
		WithinLimit: rep.MaxAbsErrNS <= thresholdNS,
	}
}

func percentile(vals []model.Ticks, pct int) model.Ticks {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]model.Ticks(nil), vals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := (len(sorted)*pct + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
