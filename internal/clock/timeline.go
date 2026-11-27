// Package clock defines the integer sample clock shared by parsing, audio
// processing and muxing stages. Every duration in the pipeline is expressed
// as a whole number of samples at a known rate; floating point is never used
// to advance media time.
package clock

// Timeline maps a monotonic integer sample position onto wall-clock time for
// a single material processed at a fixed sample rate.
type Timeline struct {
	rate   uint32
	sample uint64
}

// NewTimeline starts a timeline at sample zero for the given rate.
func NewTimeline(rate uint32) *Timeline {
	if rate == 0 {
		panic("clock: zero sample rate")
	}
	return &Timeline{rate: rate}
}

// Rate returns the timeline sample rate in Hz.
func (t *Timeline) Rate() uint32 { return t.rate }

// Position returns the next sample position to be written.
func (t *Timeline) Position() uint64 { return t.sample }

// Advance moves the clock forward by n samples and returns the start
// position of the advanced span. Duplicate packets must not call Advance.
func (t *Timeline) Advance(n uint32) (start uint64) {
	start = t.sample
	t.sample += uint64(n)
	return start
}

// Elapsed returns the number of samples accumulated so far.
func (t *Timeline) Elapsed() uint64 { return t.sample }

// Micros converts a sample span at the timeline rate to microseconds using
// integer rounding to the nearest microsecond.
func (t *Timeline) Micros(samples uint64) uint64 {
	return (samples*1_000_000 + uint64(t.rate)/2) / uint64(t.rate)
}

// Millis converts a sample span to whole milliseconds, rounded.
func (t *Timeline) Millis(samples uint64) uint64 {
	return (samples*1_000 + uint64(t.rate)/2) / uint64(t.rate)
}

// ConvertRate expresses a sample count measured at fromRate as the nearest
// whole sample count at toRate. It is used only for bookkeeping conversions
// (e.g. reporting input vs output sample counts); actual resampling is done
// by the audio package with filter state.
func ConvertRate(samples uint64, fromRate, toRate uint32) uint64 {
	if fromRate == 0 || toRate == 0 {
		panic("clock: zero sample rate in ConvertRate")
	}
	num := samples * uint64(toRate)
	return (num + uint64(fromRate)/2) / uint64(fromRate)
}
