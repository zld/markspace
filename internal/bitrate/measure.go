// Package bitrate measures target versus actual bitrate with a sliding window
// and reports the deviation percentage.
package bitrate

import (
	"fmt"
	"math"
	"sort"
)

// Observation is one recovered byte span located on the shared timeline.
type Observation struct {
	StartMs int64
	EndMs   int64
	Bytes   int64
}

// Window is one sliding-window rate estimate.
type Window struct {
	StartMs int64
	EndMs   int64
	Bytes   int64
	Kbps    float64
}

// Report is a deterministic bitrate comparison.
type Report struct {
	TargetKbps float64
	ActualKbps float64
	ErrorPct   float64
	WindowMs   int64
	Windows    []Window
	WindowMin  float64
	WindowMax  float64
	DurationMs int64
	TotalBytes int64
	MeasuredMs int64
}

// Measure computes the actual rate over the covered duration and per-window
// rates. Coverage is the union of observations clipped to [0,durationMs];
// windows with no covered time are omitted rather than reported as zero.
func Measure(targetKbps float64, durationMs, windowMs int64, obs []Observation) Report {
	r := Report{TargetKbps: targetKbps, WindowMs: windowMs, DurationMs: durationMs}
	if windowMs <= 0 {
		windowMs = 1000
		r.WindowMs = windowMs
	}
	clipped := make([]Observation, 0, len(obs))
	for _, o := range obs {
		s, e := o.StartMs, o.EndMs
		if s < 0 {
			s = 0
		}
		if e > durationMs {
			e = durationMs
		}
		if e <= s || o.Bytes <= 0 {
			continue
		}
		clipped = append(clipped, Observation{StartMs: s, EndMs: e, Bytes: o.Bytes})
		r.TotalBytes += o.Bytes
	}

	for start := int64(0); start < durationMs; start += windowMs {
		end := start + windowMs
		if end > durationMs {
			end = durationMs
		}
		var bytes int64
		var covered int64
		for _, o := range clipped {
			s, e := max64(o.StartMs, start), min64(o.EndMs, end)
			if e <= s {
				continue
			}
			// Distribute span bytes uniformly by time.
			span := o.EndMs - o.StartMs
			if span <= 0 {
				continue
			}
			part := int64(math.Round(float64(o.Bytes) * float64(e-s) / float64(span)))
			bytes += part
			covered += e - s
		}
		if covered < min64(windowMs, end-start) {
			continue // incomplete window: do not overstate the rate
		}
		kbps := kbpsOf(bytes, covered)
		r.Windows = append(r.Windows, Window{StartMs: start, EndMs: end, Bytes: bytes, Kbps: kbps})
	}

	// The actual rate is measured over the recovered interval only. Bytes in a
	// retained gap were never recovered, so charging the gap against the
	// repaired span would misreport the asset's real rate. Overlapping spans
	// are unioned so shared time is never counted twice.
	var coveredMs int64
	curStart, curEnd := int64(-1), int64(-1)
	spans := append([]Observation(nil), clipped...)
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartMs != spans[j].StartMs {
			return spans[i].StartMs < spans[j].StartMs
		}
		return spans[i].EndMs < spans[j].EndMs
	})
	for _, o := range spans {
		if curStart < 0 {
			curStart, curEnd = o.StartMs, o.EndMs
			continue
		}
		if o.StartMs > curEnd {
			coveredMs += curEnd - curStart
			curStart, curEnd = o.StartMs, o.EndMs
			continue
		}
		if o.EndMs > curEnd {
			curEnd = o.EndMs
		}
	}
	if curStart >= 0 {
		coveredMs += curEnd - curStart
	}
	if coveredMs <= 0 {
		coveredMs = durationMs
	}
	r.MeasuredMs = coveredMs
	r.ActualKbps = kbpsOf(r.TotalBytes, coveredMs)
	if targetKbps > 0 {
		r.ErrorPct = (r.ActualKbps - targetKbps) / targetKbps * 100
	}
	if len(r.Windows) > 0 {
		r.WindowMin, r.WindowMax = r.Windows[0].Kbps, r.Windows[0].Kbps
		for _, w := range r.Windows[1:] {
			r.WindowMin = math.Min(r.WindowMin, w.Kbps)
			r.WindowMax = math.Max(r.WindowMax, w.Kbps)
		}
	}
	return r
}

// kbpsOf converts bytes over a span of milliseconds to kilobits per second.
func kbpsOf(bytes, ms int64) float64 {
	if ms <= 0 {
		return 0
	}
	return float64(bytes) * 8 / (float64(ms) / 1000) / 1000
}

// WithinTolerance reports whether the signed error is within the band.
func (r Report) WithinTolerance(pct float64) bool {
	return math.Abs(r.ErrorPct) <= pct
}

// String renders the report with stable two-decimal formatting.
func (r Report) String() string {
	return fmt.Sprintf("target=%.2f actual=%.2f error=%+.2f%% windows=%d min=%.2f max=%.2f",
		r.TargetKbps, r.ActualKbps, r.ErrorPct, len(r.Windows), r.WindowMin, r.WindowMax)
}

// SortedObs normalizes observations into deterministic order.
func SortedObs(obs []Observation) []Observation {
	out := append([]Observation(nil), obs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMs != out[j].StartMs {
			return out[i].StartMs < out[j].StartMs
		}
		return out[i].EndMs < out[j].EndMs
	})
	return out
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
