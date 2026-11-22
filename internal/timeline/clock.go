// Package timeline provides the single rational time base shared by the
// transport, index, demux and remux stages. All conversions use integer
// arithmetic so repeated runs and long streams never accumulate float drift.
package timeline

import (
	"errors"
	"math/bits"
	"strconv"
	"time"

	"mediadist/internal/merr"
)

// MaxTimebase bounds a tick rate to 1 GHz, which is more than every container
// and codec clock in scope and keeps 64-bit intermediate products safe.
const MaxTimebase = 1_000_000_000

// Rational is a normalized exact fraction num/den with den > 0.
type Rational struct {
	Num int64
	Den int64
}

// NewRational normalizes num/den. A zero denominator is rejected.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("timeline: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), den)
	if g > 1 {
		num /= g
		den /= g
	}
	return Rational{Num: num, Den: den}, nil
}

// Float is only for reporting. Pipeline decisions never use it.
func (r Rational) Float() float64 { return float64(r.Num) / float64(r.Den) }

func (r Rational) String() string {
	return strconv.FormatInt(r.Num, 10) + "/" + strconv.FormatInt(r.Den, 10)
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

const maxInt64 = int64(^uint64(0) >> 1)

// mulDiv64 computes floor(x*y/d) exactly using a 128-bit intermediate.
// It returns an error when the mathematical result does not fit in int64.
func mulDiv64(x, y, d int64) (int64, error) {
	if d <= 0 {
		return 0, errors.New("timeline: non-positive divisor")
	}
	if x < 0 || y < 0 {
		return 0, errors.New("timeline: negative operand")
	}
	hi, lo := bits.Mul64(uint64(x), uint64(y))
	if hi >= uint64(d) {
		return 0, errors.New("timeline: multiplication overflow")
	}
	q, rem := bits.Div64(hi, lo, uint64(d))
	if q > uint64(maxInt64) {
		return 0, errors.New("timeline: result overflow")
	}
	if rem != 0 {
		// Flooring is intentional for exact integer tick conversion.
	}
	return int64(q), nil
}

// Scale converts ticks between two integer clock rates exactly, flooring any
// remainder. Example: Scale(90000, 90000, 1000) == 1000.
func Scale(ticks int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, errors.New("timeline: zero timescale")
	}
	if ticks < 0 {
		return 0, errors.New("timeline: negative ticks")
	}
	if from == to {
		return ticks, nil
	}
	return mulDiv64(ticks, int64(to), int64(from))
}

// ScaleRound converts ticks between clocks, rounding halves up. Use it for
// presentation timestamps where the nearest representable tick is desired.
func ScaleRound(ticks int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, errors.New("timeline: zero timescale")
	}
	if ticks < 0 {
		return 0, errors.New("timeline: negative ticks")
	}
	if from == to {
		return ticks, nil
	}
	// Rounding needs the remainder of one 128-bit product. Operands are
	// bounded by MaxTimebase and container durations, so a second exact
	// product is fine; we derive the remainder as prod - q*d.
	hi, lo := bits.Mul64(uint64(ticks), uint64(to))
	if hi >= uint64(from) {
		return 0, errors.New("timeline: multiplication overflow")
	}
	q, _ := bits.Div64(hi, lo, uint64(from))
	if q > uint64(maxInt64) {
		return 0, errors.New("timeline: result overflow")
	}
	qdHi, qdLo := bits.Mul64(q, uint64(from))
	remHi, remLo := bits.Sub64(hi, qdHi, 0)
	remLo, borrow := bits.Sub64(lo, qdLo, 0)
	if borrow != 0 {
		remHi--
	}
	if remHi != 0 {
		return 0, errors.New("timeline: remainder overflow")
	}
	if remLo >= uint64(from)-remLo {
		q++
	}
	return int64(q), nil
}

// mulRem returns (x*y) mod d for non-negative operands, d > 0, or false when
// either the product or the divisor is out of the safe range.
func mulRem(x, y, d int64) (int64, bool) {
	if x < 0 || y < 0 || d <= 0 {
		return 0, false
	}
	const limit = int64(1) << 62
	if x == 0 || (y != 0 && x <= limit/y) {
		return (x * y) % d, true
	}
	return 0, false
}

// Clock extends wrapped raw timestamps into a monotonic int64 tick space and
// converts between the stream timebase and nanoseconds. A zero Start treats
// the first observed tick as the origin; a non-zero Start makes the origin
// explicit for streams whose first timestamp is not zero.
type Clock struct {
	Timescale uint32
	WrapBits  uint
	Start     int64

	ref    int64
	primed bool
}

// NewClock validates timescale and wrap width. wrapBits 0 means the 32-bit RTP
// timestamp; accepted range is 8..32 to keep half-range disambiguation sane.
func NewClock(timescale uint32, wrapBits uint) (*Clock, error) {
	if timescale == 0 || timescale > MaxTimebase {
		return nil, merr.New(merr.StageTimeline, "bad_timescale", merr.NoSpan(0), false,
			"timescale %d outside 1..%d", timescale, MaxTimebase)
	}
	if wrapBits == 0 {
		wrapBits = 32
	}
	if wrapBits < 8 || wrapBits > 32 {
		return nil, merr.New(merr.StageTimeline, "bad_wrap_bits", merr.NoSpan(0), false,
			"wrap bits %d outside 8..32", wrapBits)
	}
	return &Clock{Timescale: timescale, WrapBits: wrapBits}, nil
}

func (c *Clock) wrapSpan() int64 { return int64(1) << c.WrapBits }

// ExpandFrom extends raw relative to an absolute reference tick. If the
// candidate is more than half a wrap away from ref it is shifted by one wrap.
// This tolerates reordering while keeping the result deterministic.
func (c *Clock) ExpandFrom(raw uint32, ref int64) int64 {
	if c == nil {
		return int64(raw)
	}
	w := c.wrapSpan()
	base := ref - mod(ref, w)
	cand := base + int64(raw)
	if cand-ref > w/2 {
		cand -= w
	} else if ref-cand > w/2 {
		cand += w
	}
	for cand < 0 {
		cand += w
	}
	return cand
}

// Expand is the stateful variant used while walking one stream in arrival
// order. The first call primes the reference with the raw value plus Start.
func (c *Clock) Expand(raw uint32) int64 {
	if c == nil {
		return int64(raw)
	}
	if !c.primed {
		c.ref = int64(raw) + c.Start
		c.primed = true
		return c.ref
	}
	c.ref = c.ExpandFrom(raw, c.ref)
	return c.ref
}

// Reset clears the stateful reference so a new segment starts at Start.
func (c *Clock) Reset() {
	c.ref = 0
	c.primed = false
}

// ToNS converts absolute ticks in this clock to nanoseconds by exact rational
// scaling. Sub-nanosecond remainders are floored.
func (c *Clock) ToNS(ticks int64) int64 {
	if c == nil {
		return 0
	}
	v, _ := mulDiv64(ticks, int64(time.Second), int64(c.Timescale))
	return v
}

// FromNS converts nanoseconds to ticks in this clock, rounding down.
func (c *Clock) FromNS(ns int64) int64 {
	if c == nil || ns < 0 {
		return 0
	}
	v, _ := mulDiv64(ns, int64(c.Timescale), int64(time.Second))
	return v
}

// TickDuration returns the wall-clock duration of ticks.
func (c *Clock) TickDuration(ticks int64) time.Duration {
	if c == nil {
		return 0
	}
	return time.Duration(c.ToNS(ticks))
}

// RationalRate returns the seconds-per-tick ratio as an exact fraction.
func (c *Clock) RationalRate() Rational {
	return Rational{Num: 1, Den: int64(c.Timescale)}
}

// OriginNS is the wall-clock position of tick 0 for streams with explicit
// non-zero Start.
func (c *Clock) OriginNS() int64 { return c.ToNS(c.Start) }

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// Delta returns the forward distance from prev to cur in the raw timestamp
// space, assuming cur is the later value. It is the wrap-safe subtraction used
// by the reorder cache.
func Delta(prev, cur uint32, wrapBits uint) uint32 {
	mask := uint32(1)<<wrapBits - 1
	return (cur - prev) & mask
}
