package align

import (
	"math"
	"sort"

	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
)

// Interval is one sample-to-sample span derived from real timestamps. Nominal
// frames are computed from the actual duration, never from an average frame
// rate, so 23.976-style pulldown and true VFR both remain faithful.
type Interval struct {
	FromIndex    int
	ToIndex      int
	Start        clock.Rational
	End          clock.Rational
	DurationMs   float64
	NominalFPS   float64
	Anomalous    bool
	DeviationPct float64
}

// VFROptions parametrizes interval derivation.
type VFROptions struct {
	Timescale    int64   // media ticks per second of the sample clock
	MedianMs     float64 // expected frame interval, zero => derive from data
	TolerancePct float64 // anomaly threshold, default 20
}

// DeriveIntervals converts sample DTS values (media ticks) into intervals.
// A non-monotone timestamp or a missing timescale yields a structured fault
// with the sample index, never a silent clamp.
func DeriveIntervals(dts []int64, srcOffset int64, opts VFROptions) ([]Interval, []*diag.Fault, error) {
	if opts.Timescale <= 0 {
		return nil, nil, diag.New(diag.CodeMissingBase, srcOffset, "vfr analysis needs a positive timescale")
	}
	if len(dts) < 2 {
		return nil, nil, diag.New(diag.CodeMissingBase, srcOffset,
			"vfr analysis needs >=2 samples, got %d", len(dts))
	}
	var faults []*diag.Fault
	var raws []float64
	var ivs []Interval
	for i := 1; i < len(dts); i++ {
		delta := dts[i] - dts[i-1]
		if delta <= 0 {
			f := diag.New(diag.CodeTimestampRollback, srcOffset+int64(i),
				"sample %d timestamp does not advance (%d -> %d)", i, dts[i-1], dts[i])
			f.WithMedia(float64(dts[i]) * 1000 / float64(opts.Timescale))
			faults = append(faults, f)
			continue
		}
		ms := float64(delta) * 1000 / float64(opts.Timescale)
		raws = append(raws, ms)
		ivs = append(ivs, Interval{
			FromIndex: i - 1, ToIndex: i,
			Start:      clock.FromTicks(dts[i-1], opts.Timescale),
			End:        clock.FromTicks(dts[i], opts.Timescale),
			DurationMs: ms,
			NominalFPS: 1000 / ms,
		})
	}
	if len(ivs) == 0 {
		return nil, faults, diag.New(diag.CodeMissingBase, srcOffset, "no advancing sample intervals")
	}
	med := opts.MedianMs
	if med <= 0 {
		med = median(raws)
	}
	tol := opts.TolerancePct
	if tol <= 0 {
		tol = 20
	}
	for i := range ivs {
		dev := math.Abs(ivs[i].DurationMs-med) / med * 100
		ivs[i].DeviationPct = dev
		if dev > tol {
			ivs[i].Anomalous = true
			f := diag.New(diag.CodeVFRGap, srcOffset+int64(ivs[i].ToIndex),
				"interval %d..%d is %.3f ms (median %.3f ms, deviation %.1f%%)",
				ivs[i].FromIndex, ivs[i].ToIndex, ivs[i].DurationMs, med, dev)
			f.WithMedia(ivs[i].End.Millis())
			faults = append(faults, f)
		}
	}
	return ivs, faults, nil
}

// MeanFPS is the duration-weighted rate over all intervals.
func MeanFPS(ivs []Interval) float64 {
	var dur float64
	for _, iv := range ivs {
		dur += iv.DurationMs
	}
	if dur == 0 {
		return 0
	}
	return float64(len(ivs)) * 1000 / dur
}

// MedianFPS is the rate implied by the median interval.
func MedianFPS(ivs []Interval) float64 {
	if len(ivs) == 0 {
		return 0
	}
	ds := make([]float64, len(ivs))
	for i, iv := range ivs {
		ds[i] = iv.DurationMs
	}
	m := median(ds)
	if m == 0 {
		return 0
	}
	return 1000 / m
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}
