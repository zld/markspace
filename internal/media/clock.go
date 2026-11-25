// Package media defines the shared sample-based media clock and the time
// conversion rules used by every subsystem.
//
// All media positions inside the project are expressed as a number of
// samples relative to the stream origin. Conversions to and from RTP
// timestamp units use one fixed rounding rule (round half away from zero)
// so that a value converted back and forth drifts by at most one sample.
package media

// Clock is a sample-based media clock. Rate is samples per second.
type Clock struct {
	Rate uint32
}

// NewClock returns a clock running at the given sample rate.
func NewClock(rate uint32) Clock {
	return Clock{Rate: rate}
}

// SamplesToTimestamp converts a sample position to RTP timestamp units
// (units of clockRate per second), rounding half away from zero.
func (c Clock) SamplesToTimestamp(samples int64, clockRate uint32) uint32 {
	if clockRate == 0 {
		return 0
	}
	return uint32(divRound(samples*int64(clockRate), int64(c.Rate)))
}

// TimestampToSamples converts an RTP timestamp delta into a sample count,
// rounding half away from zero.
func (c Clock) TimestampToSamples(timestampDelta int64, clockRate uint32) int64 {
	if clockRate == 0 {
		return 0
	}
	return divRound(timestampDelta*int64(c.Rate), int64(clockRate))
}

// Duration returns the whole number of samples covered by the given range.
func (c Clock) Duration(startSample, endSample int64) int64 {
	return endSample - startSample
}

// divRound divides numerator by denominator rounding half away from zero.
// denominator must be positive.
func divRound(numerator, denominator int64) int64 {
	if denominator <= 0 {
		return 0
	}
	if numerator >= 0 {
		return (numerator + denominator/2) / denominator
	}
	return -((-numerator + denominator/2) / denominator)
}

// AbsInt64 returns the absolute value of v.
func AbsInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
