// Package report aggregates pipeline events into a stable JSON summary.
package report

import (
	"encoding/json"
	"io"
	"math"
	"sort"

	"streamlab/internal/receive"
)

// Summary is the terminal report. Field names are stable across runs.
type Summary struct {
	ReceivedPackets       int            `json:"received_packets"`
	DroppedPackets        map[string]int `json:"dropped_packets"`
	PlayedFrames          int            `json:"played_frames"`
	ConcealedFrames       int            `json:"concealed_frames"`
	ConcealedRatio        float64        `json:"concealed_ratio"`
	ConcealRunsMax        int            `json:"conceal_runs_max"`
	ConcealSingleRuns     int            `json:"conceal_single_runs"`
	ConcealMultiRuns      int            `json:"conceal_multi_runs"`
	BoundaryEnergyJumpP95 float64        `json:"boundary_energy_jump_p95"`
	LatencyMSP50          float64        `json:"latency_ms_p50"`
	LatencyMSP95          float64        `json:"latency_ms_p95"`
	JitterMS              float64        `json:"jitter_ms"`
	BufferWatermarks      []int          `json:"buffer_watermarks"`
}

// Summarize builds the report from pipeline state. The concealer is
// flushed so a trailing gap still closes its run.
func Summarize(p *receive.Pipeline) Summary {
	conc := p.Concealer()
	conc.Flush()
	total, maxRun, single, multi, jumps := conc.Metrics()

	s := Summary{
		ReceivedPackets:       p.Received,
		DroppedPackets:        p.Dropped,
		ConcealedFrames:       total,
		ConcealRunsMax:        maxRun,
		ConcealSingleRuns:     single,
		ConcealMultiRuns:      multi,
		BoundaryEnergyJumpP95: percentile(jumps, 95),
		JitterMS:              round3(p.Controller().JitterMS()),
		BufferWatermarks:      make([]int, 0, len(p.Events)),
	}
	var lat []float64
	for _, ev := range p.Events {
		s.BufferWatermarks = append(s.BufferWatermarks, ev.Watermark)
		if ev.Concealed {
			continue
		}
		s.PlayedFrames++
		lat = append(lat, ev.LatencyMs)
	}
	if n := s.PlayedFrames + total; n > 0 {
		s.ConcealedRatio = round3(float64(total) / float64(n))
	}
	s.LatencyMSP50 = percentile(lat, 50)
	s.LatencyMSP95 = percentile(lat, 95)
	return s
}

// WriteJSON emits the summary as indented JSON.
func WriteJSON(w io.Writer, s Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	k := p / 100 * float64(len(s)-1)
	i := int(k)
	if i+1 >= len(s) {
		return round3(s[len(s)-1])
	}
	f := k - float64(i)
	return round3(s[i] + f*(s[i+1]-s[i]))
}

func round3(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1000) / 1000
}
