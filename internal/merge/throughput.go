package merge

import (
	"tracecheck/internal/model"
)

// ByteSample is one observed payload size at a capture time.
type ByteSample struct {
	At    model.Tick
	Bytes int64
}

// FlowWindow is one fixed-window throughput measurement. Throughput is
// expressed in bytes per second derived from the integer tick clock, so
// variable packet durations never enter the calculation.
type FlowWindow struct {
	Span       model.Span `json:"span"`
	Bytes      int64      `json:"bytes"`
	BytesPerSec int64     `json:"bytes_per_second"`
}

// Throughput divides samples into fixed windows of windowTicks and reports
// the throughput of each non-empty window. Bandwidth is purely a byte/time
// measurement and is kept separate from codec packet durations.
func Throughput(samples []ByteSample, windowTicks model.Tick) []FlowWindow {
	if windowTicks <= 0 || len(samples) == 0 {
		return nil
	}
	type accum struct {
		start model.Tick
		bytes int64
	}
	var windows []accum
	indexByStart := map[model.Tick]int{}
	for _, s := range samples {
		start := (s.At / windowTicks) * windowTicks
		idx, ok := indexByStart[start]
		if !ok {
			idx = len(windows)
			indexByStart[start] = idx
			windows = append(windows, accum{start: start})
		}
		windows[idx].bytes += s.Bytes
	}
	out := make([]FlowWindow, 0, len(windows))
	for _, w := range windows {
		span := model.NewSpan(w.start, w.start+windowTicks)
		// ticks -> seconds scaling in integer arithmetic.
		bps := w.bytes * int64(model.TickSecond) / int64(windowTicks)
		out = append(out, FlowWindow{Span: span, Bytes: w.bytes, BytesPerSec: bps})
	}
	return out
}

// FlowChange marks windows whose throughput differs from the previous
// window by more than changeRatio (for example 0.3 for a 30% change). The
// returned anomalies use the flow window spans directly.
func FlowChange(windows []FlowWindow, changeRatio float64) []Anomaly {
	var out []Anomaly
	for i := 1; i < len(windows); i++ {
		prev, cur := windows[i-1].BytesPerSec, windows[i].BytesPerSec
		if prev == 0 {
			continue
		}
		delta := cur - prev
		if delta < 0 {
			delta = -delta
		}
		if float64(delta)/float64(prev) <= changeRatio {
			continue
		}
		out = append(out, Anomaly{
			Span:        windows[i].Span,
			FlowChanges: 1,
			Reasons:     []string{"throughput_change"},
		})
	}
	return out
}
