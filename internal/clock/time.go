// Package clock provides one integer nanosecond time base shared by RTP
// recovery, audio/video synchronisation and manifest migration.
package clock

import (
	"fmt"
	"math"
	"time"
)

// Time is an integer number of nanoseconds since an arbitrary origin.
type Time int64

const (
	Nanosecond  Time = 1
	Microsecond Time = 1000 * Nanosecond
	Millisecond Time = 1000 * Microsecond
	Second      Time = 1000 * Millisecond
)

func (t Time) Duration() time.Duration  { return time.Duration(t) }
func (t Time) Nanos() int64             { return int64(t) }
func (t Time) Add(d time.Duration) Time { return t + Time(d) }

func (t Time) String() string { return time.Duration(t).String() }

// FromDuration converts a time.Duration to Time.
func FromDuration(d time.Duration) Time { return Time(d) }

// FromTimescale converts value/timescale to nanoseconds with rounding to the
// nearest nanosecond. It returns an error for a zero timescale or overflow.
func FromTimescale(value int64, timescale uint32) (Time, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("clock: zero timescale")
	}
	ts := int64(timescale)
	sec := value / ts
	rem := value % ts
	if sec > math.MaxInt64/int64(Second) || sec < math.MinInt64/int64(Second) {
		return 0, fmt.Errorf("clock: timescale value overflows nanoseconds")
	}
	base := sec * int64(Second)
	// rem*Second may overflow only for absurd timescales; reduce first.
	whole := rem / ts
	frac := rem % ts
	part := whole*int64(Second) + roundDiv(frac*int64(Second), ts)
	if part > 0 && base > math.MaxInt64-part {
		return 0, fmt.Errorf("clock: timescale value overflows nanoseconds")
	}
	if part < 0 && base < math.MinInt64-part {
		return 0, fmt.Errorf("clock: timescale value overflows nanoseconds")
	}
	return Time(base + part), nil
}

func roundDiv(num, den int64) int64 {
	if num >= 0 {
		return (num + den/2) / den
	}
	return (num - den/2) / den
}

// FromMilliseconds converts fractional milliseconds to nanoseconds.
func FromMilliseconds(ms float64) Time {
	return Time(math.Round(ms * float64(Millisecond)))
}

// ToMilliseconds renders a time as a floating point millisecond value.
func ToMilliseconds(t Time) float64 {
	return float64(t) / float64(Millisecond)
}

// FromSamples converts an audio sample count to nanoseconds.
func FromSamples(samples int64, sampleRate uint32) (Time, error) {
	return FromTimescale(samples, sampleRate)
}

// FromRTPTimestamp converts an extended RTP timestamp (in units of rate) to
// nanoseconds.
func FromRTPTimestamp(extended uint64, rate uint32) (Time, error) {
	if extended > math.MaxInt64 {
		return 0, fmt.Errorf("clock: RTP timestamp overflows int64")
	}
	return FromTimescale(int64(extended), rate)
}

// Guard enforces non-decreasing output time. Admit returns the supplied time
// when it is monotonic, otherwise the previous time and false. The previous
// time is never moved backwards.
type Guard struct {
	last  Time
	valid bool
}

func (g *Guard) Admit(t Time) (Time, bool) {
	if g.valid && t < g.last {
		return g.last, false
	}
	g.last = t
	g.valid = true
	return t, true
}

func (g *Guard) Last() (Time, bool) { return g.last, g.valid }

// Reset forgets the guard state.
func (g *Guard) Reset() { g.last, g.valid = 0, false }
