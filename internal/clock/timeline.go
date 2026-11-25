// Package clock provides the single media time base shared by the scanner,
// the clip cutter and the manifest writer.
//
// Media times are kept as integer ticks plus a rational tick rate so that no
// rounding is introduced until a caller explicitly asks for a duration. A
// Timeline converts between three domains:
//
//	tick       – 1/rate of a second, the unit stored in media containers
//	sample     – PCM sample frames (a frame is one sample per channel)
//	millisecond – presentation unit used by reports and event boundaries
//
// All conversions are defined to be monotone and to round toward the start of
// the interval, which keeps event intervals left-closed / right-open.
package clock

import (
	"errors"
	"fmt"
	"strconv"
)

// ErrRate is returned when a tick rate of zero would be required.
var ErrRate = errors.New("clock: tick rate must be positive")

// Rate is a rational number of ticks per second.
type Rate struct {
	Num int64
	Den int64
}

// NewRate returns a reduced-ish rate. It only rejects non-positive values;
// caller-supplied rates come from containers and are trusted otherwise.
func NewRate(num, den int64) (Rate, error) {
	if num <= 0 || den <= 0 {
		return Rate{}, ErrRate
	}
	return Rate{Num: num, Den: den}, nil
}

// MustRate is NewRate for constants known to be valid.
func MustRate(num, den int64) Rate {
	r, err := NewRate(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Float returns ticks per second as a float64.
func (r Rate) Float() float64 { return float64(r.Num) / float64(r.Den) }

// TicksPer returns how many ticks make up one unit of the other rate.
// It is integer only when the rates divide evenly.
func (r Rate) TicksPer(other Rate) (int64, bool) {
	num := r.Num * other.Den
	den := r.Den * other.Num
	if num%den != 0 {
		return 0, false
	}
	return num / den, true
}

func (r Rate) String() string {
	return strconv.FormatInt(r.Num, 10) + "/" + strconv.FormatInt(r.Den, 10)
}

// Timeline binds a tick rate and a PCM sample rate. The zero value is not
// usable; build one with New.
type Timeline struct {
	rate   Rate
	sample int64 // sample frames per second (e.g. 48000)
}

// New validates inputs and returns a Timeline.
func New(rate Rate, sampleHz int) (*Timeline, error) {
	if rate.Num <= 0 || rate.Den <= 0 {
		return nil, ErrRate
	}
	if sampleHz <= 0 {
		return nil, fmt.Errorf("clock: sample rate must be positive, got %d", sampleHz)
	}
	return &Timeline{rate: rate, sample: int64(sampleHz)}, nil
}

// NewForSampleRate builds a timeline whose tick rate equals the sample rate,
// the common case for PCM-derived containers.
func NewForSampleRate(sampleHz int) *Timeline {
	t, err := New(Rate{Num: int64(sampleHz), Den: 1}, sampleHz)
	if err != nil {
		panic(err)
	}
	return t
}

// Rate returns the tick rate.
func (t *Timeline) Rate() Rate { return t.rate }

// SampleRate returns PCM sample frames per second.
func (t *Timeline) SampleRate() int { return int(t.sample) }

// SamplePeriod returns one sample frame expressed in ticks.
func (t *Timeline) SamplePeriod() (Rate, bool) {
	return Rate{Num: t.rate.Num, Den: t.rate.Den * t.sample}, true
}

// SamplePeriodMillis returns duration of one sample frame in milliseconds.
func (t *Timeline) SamplePeriodMillis() float64 {
	return 1000.0 * float64(t.rate.Den) / (float64(t.rate.Num) * float64(t.sample))
}

// TicksToSamples converts ticks to whole sample frames. The result is floored
// so that a sample index never points past the requested tick.
func (t *Timeline) TicksToSamples(ticks int64) int64 {
	num := ticks * t.rate.Den * t.sample
	den := t.rate.Num
	return floorDiv(num, den)
}

// SamplesToTicks converts whole sample frames to ticks. The conversion is
// exact only when the tick rate is a multiple of the sample rate; otherwise
// the result is floored.
func (t *Timeline) SamplesToTicks(samples int64) int64 {
	return floorDiv(samples*t.rate.Num, t.sample*t.rate.Den)
}

// TicksToMillis converts ticks to milliseconds, floored.
func (t *Timeline) TicksToMillis(ticks int64) int64 {
	return floorDiv(ticks*1000*t.rate.Den, t.rate.Num)
}

// MillisToTicks converts milliseconds to ticks, floored.
func (t *Timeline) MillisToTicks(ms int64) int64 {
	return floorDiv(ms*t.rate.Num, 1000*t.rate.Den)
}

// AlignDownSamples returns the sample index whose tick start is the latest
// sample-aligned instant not after ticks. This is the audio-boundary rule:
// boundaries are closed on their own sample frame.
func (t *Timeline) AlignDownSamples(ticks int64) int64 {
	return t.TicksToSamples(ticks)
}

// AlignUpSamples returns the sample index at or after ticks, used where a
// consumer needs to guarantee it covers the requested instant.
func (t *Timeline) AlignUpSamples(ticks int64) int64 {
	s := t.TicksToSamples(ticks)
	if t.SamplesToTicks(s) < ticks {
		s++
	}
	return s
}

// DeltaMillisAbs reports |a-b| in milliseconds.
func (t *Timeline) DeltaMillisAbs(a, b int64) int64 {
	if a > b {
		return t.TicksToMillis(a - b)
	}
	return t.TicksToMillis(b - a)
}

// WrapsForward reports whether stepping from prev to next crosses the
// signed-32-bit wrap point in the forward direction, i.e. a counter rollover.
func WrapsForward(prev, next int64) bool {
	return prev >= 0 && next < prev && prev-next > (1<<31)
}

// UnwrapSigned expands a 32-bit counter difference into an absolute delta
// using the standard modular interpretation.
func UnwrapSigned(diff int64) int64 {
	diff &= 0xFFFFFFFF
	if diff >= 1<<31 {
		diff -= 1 << 32
	}
	return diff
}

func floorDiv(a, b int64) int64 {
	q := a / b
	r := a % b
	if r != 0 && (r < 0) != (b < 0) {
		q--
	}
	return q
}

// TicksToMillisSafe converts ticks at an arbitrary rate to milliseconds.
func TicksToMillisSafe(ticks, perSecond int64) int64 {
	if perSecond <= 0 {
		return 0
	}
	return floorDiv(ticks*1000, perSecond)
}
