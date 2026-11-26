// Package clock provides the shared rational time base used by the WAV and RTP
// subsystems. All conversions use exact integer arithmetic and report overflow
// as a fault instead of silently wrapping.
package clock

import (
	"math"
	"math/bits"

	"example.com/mediavault/internal/fault"
)

// Rate is an exact rational sample rate expressed in samples per second.
type Rate struct {
	Num int64
	Den int64
}

// NewRate builds a reduced, positive rate. A zero or negative value yields an
// invalid rate so callers can reject it with a stable code.
func NewRate(num, den int64) Rate {
	if num <= 0 || den <= 0 {
		return Rate{}
	}
	g := gcd(num, den)
	return Rate{Num: num / g, Den: den / g}
}

// Valid reports whether the rate can be used in conversions.
func (r Rate) Valid() bool { return r.Num > 0 && r.Den > 0 }

// NanoPerSample returns the duration of one sample in nanoseconds, truncated.
// It is only a convenience for display; use DurationOfSamples for exact math.
func (r Rate) NanoPerSample() int64 {
	if !r.Valid() {
		return 0
	}
	return 1_000_000_000 * r.Den / r.Num
}

// DurationOfSamples converts a sample count to nanoseconds.
func (r Rate) DurationOfSamples(n int64) (int64, error) {
	if !r.Valid() {
		return 0, fault.New(fault.CodeArgs, fault.StageScanned, -1, "invalid sample rate %d/%d", r.Num, r.Den)
	}
	if n < 0 {
		return 0, fault.New(fault.CodeArgs, fault.StageScanned, -1, "negative sample count %d", n)
	}
	hi, lo := bits.Mul64(uint64(n), uint64(r.Den))
	if hi != 0 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "sample duration overflows: n=%d den=%d", n, r.Den)
	}
	if lo > math.MaxUint64/1_000_000_000 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "nanosecond conversion overflows: n=%d", n)
	}
	return int64(lo * 1_000_000_000 / uint64(r.Num)), nil
}

// SamplesAt converts a nanosecond instant to a sample position, truncating
// towards zero. It is the inverse of DurationOfSamples up to one sample.
func (r Rate) SamplesAt(ns int64) (int64, error) {
	if !r.Valid() {
		return 0, fault.New(fault.CodeArgs, fault.StageScanned, -1, "invalid sample rate %d/%d", r.Num, r.Den)
	}
	if ns < 0 {
		return 0, fault.New(fault.CodeArgs, fault.StageScanned, -1, "negative instant %d", ns)
	}
	hi, lo := bits.Mul64(uint64(ns), uint64(r.Num))
	if hi != 0 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "instant overflows: ns=%d rate=%d/%d", ns, r.Num, r.Den)
	}
	den := 1_000_000_000 * uint64(r.Den)
	if den/1_000_000_000 != uint64(r.Den) {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "rate denominator overflows: %d", r.Den)
	}
	return int64(lo / den), nil
}

// PPMDelta returns the parts-per-million difference between actual and
// expected. It is the drift measure used to isolate assets.
func PPMDelta(actual, expected int64) (int64, error) {
	if expected <= 0 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "non-positive expected advance %d", expected)
	}
	diff := actual - expected
	if diff == math.MinInt64 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "drift difference overflows")
	}
	hi, lo := bits.Mul64(abs64(diff), 1_000_000)
	if hi != 0 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageScanned, -1, "ppm product overflows")
	}
	ppm := int64(lo / uint64(expected))
	if diff < 0 {
		ppm = -ppm
	}
	return ppm, nil
}

// Unwrapper expands 32-bit RTP timestamps to 64 bits. Expansion only happens
// at a determinable wrap point; a smaller timestamp that cannot be explained
// as a wrap is reported as a backward jump and isolates the asset.
type Unwrapper struct {
	last    uint32
	base    uint64
	started bool
	wraps   int
}

// NewUnwrapper returns a ready unwrapper.
func NewUnwrapper() *Unwrapper { return &Unwrapper{} }

// Unwrap consumes the next raw timestamp and returns its expanded value plus
// whether a 32-bit wrap was crossed. It never guesses at ambiguous jumps.
func (u *Unwrapper) Unwrap(raw uint32) (uint64, bool, error) {
	if !u.started {
		u.started = true
		u.last = raw
		return uint64(raw), false, nil
	}
	if raw == u.last {
		return u.base + uint64(raw), false, nil
	}
	if raw > u.last {
		return u.base + uint64(raw), false, nil
	}
	// raw < last: only a wrap (half the timestamp space or bigger) is valid.
	if uint64(u.last)-uint64(raw) > 1<<31 {
		u.base += 1 << 32
		u.last = raw
		u.wraps++
		return u.base + uint64(raw), true, nil
	}
	return 0, false, fault.New(fault.CodeRTPBackward, fault.StageNormalized, -1,
		"timestamp moved backward from %d to %d", u.last, raw)
}

// Wraps reports how many 32-bit wraps have been expanded.
func (u *Unwrapper) Wraps() int { return u.wraps }

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}
