// Package media defines the unified time base and shared value types used by
// every subsystem. All WAV and RTP observations are converted onto the same
// PCM-sample axis before analysis, budgeting and reporting.
package media

import "errors"

// TimeBase binds the local PCM sample clock and the RTP media clock.
// SampleRate is the number of PCM frames per second; RTPClock is the number
// of RTP timestamp ticks per second advertised for the stream.
type TimeBase struct {
	SampleRate uint32
	RTPClock   uint32
}

// Errors returned by clock conversion. They are never silently corrected:
// callers must surface them through the segment error model.
var (
	ErrNoRTPClock         = errors.New("rtp clock rate is zero: cannot convert RTP timestamps")
	ErrInexactConversion  = errors.New("rtp-to-sample conversion would truncate ticks")
	ErrZeroSampleRate     = errors.New("sample rate is zero: cannot express the duration on the time base")
)

// RTPToSamples converts RTP timestamp ticks onto the unified PCM-sample axis.
// It returns ErrNoRTPClock when the clock is unknown and ErrInexactConversion
// when the scaled value would lose ticks instead of rounding.
func (tb TimeBase) RTPToSamples(ticks uint64) (uint64, error) {
	if tb.RTPClock == 0 {
		return 0, ErrNoRTPClock
	}
	if tb.SampleRate == 0 {
		return 0, ErrZeroSampleRate
	}
	num := ticks * uint64(tb.SampleRate)
	if rem := num % uint64(tb.RTPClock); rem != 0 {
		return 0, ErrInexactConversion
	}
	return num / uint64(tb.RTPClock), nil
}

// SamplesToRTP converts PCM samples back to RTP ticks with the same exactness
// guarantees as RTPToSamples.
func (tb TimeBase) SamplesToRTP(samples uint64) (uint64, error) {
	if tb.RTPClock == 0 {
		return 0, ErrNoRTPClock
	}
	if tb.SampleRate == 0 {
		return 0, ErrZeroSampleRate
	}
	num := samples * uint64(tb.RTPClock)
	if rem := num % uint64(tb.SampleRate); rem != 0 {
		return 0, ErrInexactConversion
	}
	return num / uint64(tb.SampleRate), nil
}

// SamplesToMillis maps a sample count to whole milliseconds, truncating the
// remainder. Milliseconds are only used for budget accounting, never to
// re-index samples.
func (tb TimeBase) SamplesToMillis(samples uint64) (uint64, error) {
	if tb.SampleRate == 0 {
		return 0, ErrZeroSampleRate
	}
	return samples * 1000 / uint64(tb.SampleRate), nil
}

// MillisToSamples is the inverse of SamplesToMillis; it rounds up so a budget
// expressed in milliseconds never silently under-allocates.
func (tb TimeBase) MillisToSamples(ms uint64) (uint64, error) {
	if tb.SampleRate == 0 {
		return 0, ErrZeroSampleRate
	}
	num := ms * uint64(tb.SampleRate)
	if rem := num % 1000; rem != 0 {
		return num/1000 + 1, nil
	}
	return num / 1000, nil
}

// wrapDelta interprets cur as the next modular value after the already
// extended prev value and returns the signed distance on the monotonic axis.
// width is the counter width in bits (16 for RTP sequence numbers, 32 for
// RTP timestamps). A forward distance greater than half the counter space is
// treated as a backward step instead of a huge jump.
func wrapDelta(prev uint64, cur uint64, width uint) int64 {
	mod := uint64(1) << width
	half := mod / 2
	delta := (cur - prev) & (mod - 1)
	if delta >= half {
		delta -= mod
	}
	return int64(delta)
}

// Extend16 projects a wrapping 16-bit counter value onto a monotonic 64-bit
// axis given the previously extended value. The first observed value must be
// seeded directly by the caller; Extend16 is only applied from the second
// value onward so an arbitrary initial offset is not mistaken for a jump.
func Extend16(prevExtended uint64, cur uint16) uint64 {
	d := wrapDelta(prevExtended, uint64(cur), 16)
	if d < 0 {
		return prevExtended - uint64(-d)
	}
	return prevExtended + uint64(d)
}

// Extend32 is the 32-bit counterpart of Extend16, used for RTP timestamps.
func Extend32(prevExtended uint64, cur uint32) uint64 {
	d := wrapDelta(prevExtended, uint64(cur), 32)
	if d < 0 {
		return prevExtended - uint64(-d)
	}
	return prevExtended + uint64(d)
}
