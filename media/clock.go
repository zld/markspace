package media

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// NanoDen is the denominator of the shared media time base: one nanosecond.
//
// Media Time is an exact integer count of nanoseconds, i.e. a rational value
// with the fixed denominator 1e9. Track ticks are converted with integer
// arithmetic and round-half-away-from-zero only; floating point is never used
// to accumulate a timeline, and absolute timestamps are always converted
// directly rather than by adding per-sample deltas.
const NanoDen = int64(1_000_000_000)

// Time is media time in nanoseconds (rational denominator NanoDen).
type Time int64

// FromTicks converts a track tick value to media time. The conversion uses a
// 128-bit intermediate so it is exact for the full track duration domain and
// reports overflow instead of wrapping. Negative ticks are allowed because
// composition offsets and edit shifts are signed; sample start times derived
// from stts are non-negative and are validated separately.
func FromTicks(ticks int64, timescale uint32) (Time, error) {
	if timescale == 0 {
		return 0, NewError(KindUnsupported, "", "timescale must not be zero")
	}
	if ticks < 0 {
		// Convert the magnitude and negate, so rounding is symmetric and
		// exact: -1.5 rounds to -2, matching positive rounding half away from
		// zero.
		if ticks == math.MinInt64 {
			return 0, NewError(KindLimit, "", "tick value overflows media time")
		}
		v, err := FromTicks(-ticks, timescale)
		if err != nil {
			return 0, err
		}
		return -v, nil
	}
	hi, lo := bits.Mul64(uint64(ticks), uint64(NanoDen))
	timescale64 := uint64(timescale)
	if hi >= timescale64 {
		return 0, NewError(KindLimit, "", "tick value %d at timescale %d overflows media time", ticks, timescale)
	}
	q, r := bits.Div64(hi, lo, timescale64)
	if 2*r >= timescale64 {
		if q == uint64(1<<63-1) {
			return 0, NewError(KindLimit, "", "tick value %d at timescale %d overflows media time", ticks, timescale)
		}
		q++
	}
	return Time(int64(q)), nil
}

// MustFromTicks is FromTicks for callers that already validated the timescale.
func MustFromTicks(ticks int64, timescale uint32) Time {
	t, err := FromTicks(ticks, timescale)
	if err != nil {
		return 0
	}
	return t
}

// FromDuration converts a standard duration to media time exactly.
func FromDuration(d time.Duration) Time { return Time(d.Nanoseconds()) }

// Duration returns the value as time.Duration.
func (t Time) Duration() time.Duration { return time.Duration(int64(t)) }

// Nanos returns nanoseconds.
func (t Time) Nanos() int64 { return int64(t) }

// Micros returns microseconds, rounded half away from zero. This is the
// reporting precision used in JSON output.
func (t Time) Micros() int64 { return divRound(int64(t), 1_000) }

// Millis returns milliseconds, rounded half away from zero.
func (t Time) Millis() int64 { return divRound(int64(t), 1_000_000) }

// Seconds returns whole seconds, truncated.
func (t Time) Seconds() int64 { return int64(t) / NanoDen }

// Add returns t+o; overflow yields an error.
func (t Time) Add(o Time) (Time, error) {
	sum := int64(t) + int64(o)
	if (o > 0 && sum < int64(t)) || (o < 0 && sum > int64(t)) {
		return 0, NewError(KindLimit, "", "media time addition overflow")
	}
	return Time(sum), nil
}

// Sub returns t-o; overflow yields an error.
func (t Time) Sub(o Time) (Time, error) {
	return t.Add(-o)
}

// Cmp compares two media times: -1 if t<o, 0 if equal, 1 if t>o.
func (t Time) Cmp(o Time) int {
	switch {
	case t < o:
		return -1
	case t > o:
		return 1
	default:
		return 0
	}
}

// Abs returns the absolute value.
func (t Time) Abs() Time {
	if t < 0 {
		return -t
	}
	return t
}

// String renders the value deterministically with microsecond precision, e.g.
// "2.500000s".
func (t Time) String() string {
	neg := t < 0
	v := int64(t)
	if neg {
		v = -v
	}
	us := divRound(v, 1_000)
	s := fmt.Sprintf("%d.%06ds", us/1_000_000, us%1_000_000)
	if neg {
		return "-" + s
	}
	return s
}

// MarshalJSON emits integer microseconds so reports are stable and readable.
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(t.Micros(), 10)), nil
}

// UnmarshalJSON accepts integer microseconds.
func (t *Time) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return err
	}
	*t = Time(v * 1_000)
	return nil
}

func divRound(v, d int64) int64 {
	if v >= 0 {
		return (v + d/2) / d
	}
	return -((-v + d/2) / d)
}

// ParseTime parses a duration argument such as "2s", "500ms", "1500us" or a
// bare number of seconds. Parsing is integer-only.
func ParseTime(s string) (Time, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, NewError(KindArgument, "", "empty duration")
	}
	mult := NanoDen
	for _, suffix := range []struct {
		name string
		mult int64
	}{
		{"ns", 1}, {"us", 1_000}, {"µs", 1_000}, {"ms", 1_000_000},
		{"s", NanoDen}, {"m", 60 * NanoDen}, {"h", 3600 * NanoDen},
	} {
		if strings.HasSuffix(s, suffix.name) {
			s = strings.TrimSuffix(s, suffix.name)
			mult = suffix.mult
			break
		}
	}
	if s == "" {
		return 0, NewError(KindArgument, "", "invalid duration %q", orig)
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" && (!hasFrac || fracPart == "") {
		return 0, NewError(KindArgument, "", "invalid duration %q", orig)
	}
	whole := int64(0)
	if intPart != "" {
		v, err := strconv.ParseInt(intPart, 10, 64)
		if err != nil || v < 0 {
			return 0, NewError(KindArgument, "", "invalid duration %q", orig)
		}
		whole = v
	}
	// The fractional part below one second has at most nanosecond resolution.
	if hasFrac {
		if len(fracPart) > 9 {
			fracPart = fracPart[:9]
		}
		for len(fracPart) < 9 {
			fracPart += "0"
		}
		v, err := strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return 0, NewError(KindArgument, "", "invalid duration %q", orig)
		}
		whole += divRound(v*mult, 1_000_000_000)
	} else {
		whole *= mult
	}
	if neg {
		whole = -whole
	}
	return Time(whole), nil
}

// FrameInterval is the nominal duration between samples of a constant-rate
// track; it is derived exactly from the track timescale and sample delta.
type FrameInterval struct {
	Ticks     uint32
	Timescale uint32
	media     Time
}

// NewFrameInterval builds an interval from ticks and a track timescale.
func NewFrameInterval(ticks, timescale uint32) (FrameInterval, error) {
	if timescale == 0 {
		return FrameInterval{}, NewError(KindUnsupported, "", "timescale must not be zero")
	}
	m, err := FromTicks(int64(ticks), timescale)
	if err != nil {
		return FrameInterval{}, err
	}
	return FrameInterval{Ticks: ticks, Timescale: timescale, media: m}, nil
}

// Media returns the interval as media time.
func (f FrameInterval) Media() Time { return f.media }
