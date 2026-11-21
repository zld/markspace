// Package metrics analyses received RTP for sequence anomalies and jitter.
package metrics

import (
	"math"
	"sort"
	"time"
)

// ClockRate is the RTP video clock in Hz.
const ClockRate = 90000.0

// JitterSummary is the stable jitter output. Values are milliseconds derived
// from the RTP 90 kHz clock.
type JitterSummary struct {
	Status  string  `json:"status"`
	Samples int     `json:"samples"`
	Mean    float64 `json:"mean"`
	P95     float64 `json:"p95"`
	Max     float64 `json:"max"`
}

// Jitter measures per-packet interarrival deviation: the absolute difference
// between the wall-clock gap and the RTP timestamp gap, converted to
// milliseconds. Using the 90 kHz clock and modular timestamp arithmetic keeps
// 32-bit rolls from being mistaken for jitter.
type Jitter struct {
	started     bool
	lastArrival float64
	lastTS      uint32
	samples     []float64
}

// NewJitter returns an empty calculator.
func NewJitter() *Jitter { return &Jitter{} }

// Add records an arrival. arrival is wall-clock receipt time; ts is the RTP
// timestamp, compared with modular 32-bit arithmetic so wrap is handled.
func (j *Jitter) Add(ts uint32, arrival time.Time) {
	arrivalTicks := float64(arrival.UnixNano()) * ClockRate / float64(time.Second)
	if !j.started {
		j.started = true
		j.lastArrival = arrivalTicks
		j.lastTS = ts
		return
	}
	d := (arrivalTicks - j.lastArrival) - float64(int32(ts-j.lastTS))
	if d < 0 {
		d = -d
	}
	j.samples = append(j.samples, d*1000.0/ClockRate)
	j.lastArrival = arrivalTicks
	j.lastTS = ts
}

// Summary computes mean, nearest-rank P95 and max in milliseconds.
func (j *Jitter) Summary() JitterSummary {
	out := JitterSummary{Status: JitterStatusInsufficient, Samples: len(j.samples)}
	if len(j.samples) == 0 {
		return out
	}
	sorted := append([]float64(nil), j.samples...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	out.Mean = round3(sum / float64(len(sorted)))
	out.Max = round3(sorted[len(sorted)-1])
	idx := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	out.P95 = round3(sorted[idx])
	if len(sorted) >= MinJitterSamples {
		out.Status = JitterStatusOK
	}
	return out
}

// SamplesCopy returns a copy of per-packet jitter samples in milliseconds.
func (j *Jitter) SamplesCopy() []float64 {
	return append([]float64(nil), j.samples...)
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
