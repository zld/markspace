package mediacore

import (
	"fmt"
	"math"
	"math/bits"
	"time"
)

// Timebase describes a clock as ticks per second. All media timestamps in
// this package are integer ticks on such a clock; floating point values are
// only used for reporting.
type Timebase struct {
	Timescale uint32
}

// NewTimebase returns a Timebase for the given timescale.
func NewTimebase(timescale uint32) Timebase {
	return Timebase{Timescale: timescale}
}

// Valid reports whether the timebase can be used for conversion.
func (tb Timebase) Valid() bool { return tb.Timescale != 0 }

func (tb Timebase) String() string { return fmt.Sprintf("1/%d", tb.Timescale) }

// Duration converts ticks on tb to a time.Duration using a single rounded
// rational conversion. It never accumulates rounding error.
func (tb Timebase) Duration(ticks int64) (time.Duration, error) {
	ns, err := Rescale(ticks, tb.Timescale, 1_000_000_000)
	if err != nil {
		return 0, err
	}
	return time.Duration(ns), nil
}

// Ticks converts a duration to ticks on tb.
func (tb Timebase) Ticks(d time.Duration) (int64, error) {
	return Rescale(int64(d), 1_000_000_000, tb.Timescale)
}

// Rescale converts an integer timestamp from one timescale to another with
// half-up rounding. The entire conversion is done in 128-bit arithmetic; the
// result differs from the exact value by less than one tick and, because no
// intermediate rounding is performed, repeated conversions of absolute
// positions cannot drift proportionally.
func Rescale(ticks int64, srcTimescale, dstTimescale uint32) (int64, error) {
	if srcTimescale == 0 || dstTimescale == 0 {
		return 0, fail(KindInvalidConfig, "rescale", "timescale must be non-zero (src=%d dst=%d)", srcTimescale, dstTimescale)
	}
	negative := ticks < 0
	var magnitude uint64
	if negative {
		if ticks == math.MinInt64 {
			return 0, fail(KindOverflow, "rescale", "timestamp %d cannot be negated", ticks)
		}
		magnitude = uint64(-ticks)
	} else {
		magnitude = uint64(ticks)
	}

	q, r, err := mulDiv(magnitude, uint64(dstTimescale), uint64(srcTimescale))
	if err != nil {
		return 0, wrapErr(KindOverflow, "rescale", err)
	}
	// half-up rounding: compare 2*r with src without overflowing.
	if r*2 >= uint64(srcTimescale) {
		if q == math.MaxUint64 {
			return 0, fail(KindOverflow, "rescale", "rounded result overflows")
		}
		q++
	}
	if q > math.MaxInt64 {
		return 0, fail(KindOverflow, "rescale", "result %d exceeds int64", q)
	}
	result := int64(q)
	if negative {
		result = -result
	}
	return result, nil
}

// mulDiv returns floor(a*b/d) and the remainder using 128-bit arithmetic.
// d must be non-zero. It is safe for any uint64 operands; overflow of the
// quotient is reported.
func mulDiv(a, b, d uint64) (uint64, uint64, error) {
	if d == 0 {
		return 0, 0, fmt.Errorf("division by zero")
	}
	q1 := a / d
	r1 := a % d
	// hi < d is guaranteed because r1 < d, so Div64 cannot panic.
	hi, lo := bits.Mul64(r1, b)
	q2, r2 := bits.Div64(hi, lo, d)
	hi3, lo3 := bits.Mul64(q1, b)
	if hi3 != 0 {
		return 0, 0, fmt.Errorf("quotient overflow")
	}
	sum := lo3 + q2
	if sum < lo3 {
		return 0, 0, fmt.Errorf("quotient overflow")
	}
	return sum, r2, nil
}

// RateConverter converts a sequence of integer deltas between two clocks
// while carrying the exact remainder. The sum of converted deltas equals the
// conversion of the sum of inputs (rounded once), so it introduces no
// cumulative rounding drift.
type RateConverter struct {
	Src       uint32
	Dst       uint32
	remainder uint64
	inTotal   uint64
	outTotal  uint64
}

// NewRateConverter creates a converter from srcTimescale to dstTimescale.
func NewRateConverter(srcTimescale, dstTimescale uint32) (*RateConverter, error) {
	if srcTimescale == 0 || dstTimescale == 0 {
		return nil, fail(KindInvalidConfig, "rate-converter", "timescale must be non-zero (src=%d dst=%d)", srcTimescale, dstTimescale)
	}
	return &RateConverter{Src: srcTimescale, Dst: dstTimescale}, nil
}

// Push converts one delta and returns its integer length on the destination
// clock. Remainders are carried into subsequent calls.
func (rc *RateConverter) Push(delta uint64) (uint64, error) {
	if !rc.usable() {
		return 0, fail(KindInvalidConfig, "rate-converter", "converter is not initialised")
	}
	q, r, err := mulDiv(delta, uint64(rc.Dst), uint64(rc.Src))
	if err != nil {
		return 0, wrapErr(KindOverflow, "rate-converter", err)
	}
	carry := r + rc.remainder
	if carry >= uint64(rc.Src) {
		if q == math.MaxUint64 {
			return 0, fail(KindOverflow, "rate-converter", "converted delta overflows")
		}
		q++
		carry -= uint64(rc.Src)
	}
	rc.remainder = carry
	if rc.inTotal > math.MaxUint64-delta {
		return 0, fail(KindOverflow, "rate-converter", "input total overflows")
	}
	rc.inTotal += delta
	if rc.outTotal > math.MaxUint64-q {
		return 0, fail(KindOverflow, "rate-converter", "output total overflows")
	}
	rc.outTotal += q
	return q, nil
}

func (rc *RateConverter) usable() bool {
	return rc != nil && rc.Src != 0 && rc.Dst != 0
}

// Remainder returns the currently carried remainder in source ticks.
func (rc *RateConverter) Remainder() uint64 { return rc.remainder }

// Totals returns the accumulated input and output deltas.
func (rc *RateConverter) Totals() (in, out uint64) { return rc.inTotal, rc.outTotal }

// ConvertDeltas converts every delta with carry-preserving arithmetic.
func ConvertDeltas(deltas []uint64, srcTimescale, dstTimescale uint32) ([]uint64, error) {
	rc, err := NewRateConverter(srcTimescale, dstTimescale)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, len(deltas))
	for i, delta := range deltas {
		v, err := rc.Push(delta)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Ratio returns the exact timebase ratio as a float for reporting only.
func (tb Timebase) Ratio() float64 { return float64(tb.Timescale) }
