// Package model defines the shared time base and error model used by every
// pipeline stage. All timestamps are converted to a single master clock
// (nanoseconds) as early as possible so that audio, video and RTP timelines
// can be compared without per-track timescale juggling downstream.
package model

import "fmt"

// MasterTimescale is the single shared time base: 1 tick = 1 nanosecond.
const MasterTimescale int64 = 1_000_000_000

// RTPTimescale is the clock rate used on the (simulated) RTP transport leg.
const RTPTimescale int64 = 90_000

// Rational is a rational number Num/Den used for exact timescale conversion.
type Rational struct {
	Num int64
	Den int64
}

// Timescale returns the rational for "1 second = units ticks".
func Timescale(units int64) Rational { return Rational{Num: 1, Den: units} }

// Rescale converts v ticks of the "from" timescale into ticks of the "to"
// timescale, rounding to nearest (half away from zero) so that repeated
// conversions of consecutive timestamps stay monotonic.
func Rescale(v int64, from, to Rational) int64 {
	if from.Den <= 0 || to.Den <= 0 || from.Num <= 0 || to.Num <= 0 {
		return 0
	}
	// v * from.Num * to.Den / (from.Den * to.Num)
	num := v * from.Num * to.Den
	den := from.Den * to.Num
	if num >= 0 {
		return (num + den/2) / den
	}
	return (num - den/2) / den
}

// ToMaster converts v ticks of the given timescale to master (ns) ticks.
func ToMaster(v int64, scale Rational) int64 {
	return Rescale(v, scale, Timescale(MasterTimescale))
}

// FromMaster converts master (ns) ticks into the given timescale.
func FromMaster(ns int64, scale Rational) int64 {
	return Rescale(ns, Timescale(MasterTimescale), scale)
}

// ToRTP converts master ticks to the RTP 90kHz clock.
func ToRTP(ns int64) uint32 {
	return uint32(FromMaster(ns, Timescale(RTPTimescale)))
}

// FromRTP converts an RTP 90kHz timestamp to master ticks.
func FromRTP(ts uint32) int64 {
	return ToMaster(int64(ts), Timescale(RTPTimescale))
}

// Timestamp is a point on the master timeline.
type Timestamp struct {
	Nanos int64
}

// Millis renders the timestamp in milliseconds for reports.
func (t Timestamp) Millis() float64 { return float64(t.Nanos) / 1e6 }

// DurationMillis renders a nanosecond duration as fractional milliseconds.
func DurationMillis(ns int64) float64 { return float64(ns) / 1e6 }

func (t Timestamp) String() string { return fmt.Sprintf("%.3fms", t.Millis()) }
