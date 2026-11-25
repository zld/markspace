package timebase

import (
	"errors"
	"fmt"
	"math/big"
)

// UnifiedTimescale is the audit pipeline's common tick rate: 90,000 ticks/sec.
const UnifiedTimescale uint32 = 90_000

var ErrInvalidTimestamp = errors.New("invalid timestamp")

// Timebase describes a track tick rate as Numerator/Denominator ticks per second.
type Timebase struct {
	Numerator   uint32 `json:"numerator"`
	Denominator uint32 `json:"denominator"`
}

func MP4(timescale uint32) Timebase {
	return Timebase{Numerator: timescale, Denominator: 1}
}

func MXF(numerator, denominator uint32) Timebase {
	return Timebase{Numerator: numerator, Denominator: denominator}
}

func (t Timebase) Validate() error {
	if t.Numerator == 0 || t.Denominator == 0 {
		return fmt.Errorf("%w: zero timebase %d/%d", ErrInvalidTimestamp, t.Numerator, t.Denominator)
	}
	return nil
}

// Conversion records an exact source value in the unified timebase and the
// rounding error caused by producing an integer unified tick.
type Conversion struct {
	SourceTicks       int64   `json:"source_ticks"`
	SourceNumerator   uint32  `json:"source_numerator"`
	SourceDenominator uint32  `json:"source_denominator"`
	UnifiedTicks      int64   `json:"unified_ticks"`
	ExactUnified      string  `json:"exact_unified_ticks"`
	ErrorTicks        float64 `json:"error_ticks"`
	Nanoseconds       int64   `json:"nanoseconds"`
	ExactNanoseconds  string  `json:"exact_nanoseconds"`
	ErrorNS           float64 `json:"error_ns"`
}

func rationalFloat(num *big.Int, den int64) float64 {
	f, _ := new(big.Rat).SetFrac(num, big.NewInt(den)).Float64()
	return f
}

func roundRational(num *big.Int, den int64) int64 {
	q, r := new(big.Int).QuoRem(new(big.Int).Set(num), big.NewInt(den), new(big.Int))
	twice := new(big.Int).Mul(r, big.NewInt(2))
	if twice.Cmp(big.NewInt(den)) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// Convert converts one non-negative source tick count to the unified clock.
// Invalid timestamps are rejected rather than silently changed to zero.
func Convert(tb Timebase, ticks int64) (Conversion, error) {
	if err := tb.Validate(); err != nil {
		return Conversion{}, err
	}
	if ticks < 0 {
		return Conversion{}, fmt.Errorf("%w: negative timestamp %d", ErrInvalidTimestamp, ticks)
	}

	d := int64(tb.Denominator)
	rate := int64(tb.Numerator)
	unifiedNum := big.NewInt(ticks)
	unifiedNum.Mul(unifiedNum, big.NewInt(int64(UnifiedTimescale)))
	unifiedNum.Mul(unifiedNum, big.NewInt(d))
	unifiedRounded := roundRational(unifiedNum, rate)
	unifiedExact := rationalFloat(unifiedNum, rate)

	nsNum := big.NewInt(ticks)
	nsNum.Mul(nsNum, big.NewInt(1_000_000_000))
	nsNum.Mul(nsNum, big.NewInt(d))
	nsRounded := roundRational(nsNum, rate)
	nsExact := rationalFloat(nsNum, rate)

	return Conversion{
		SourceTicks:       ticks,
		SourceNumerator:   tb.Numerator,
		SourceDenominator: tb.Denominator,
		UnifiedTicks:      unifiedRounded,
		ExactUnified:      new(big.Rat).SetFrac(unifiedNum, big.NewInt(rate)).RatString(),
		ErrorTicks:        float64(unifiedRounded) - unifiedExact,
		Nanoseconds:       nsRounded,
		ExactNanoseconds:  new(big.Rat).SetFrac(nsNum, big.NewInt(rate)).RatString(),
		ErrorNS:           float64(nsRounded) - nsExact,
	}, nil
}

// Range is a half-open [StartTicks, EndTicks) interval in unified ticks.
type Range struct {
	StartTicks int64      `json:"start_ticks"`
	EndTicks   int64      `json:"end_ticks"`
	Timescale  uint32     `json:"timescale"`
	StartNS    int64      `json:"start_ns"`
	EndNS      int64      `json:"end_ns"`
	Start      Conversion `json:"start_conversion"`
	End        Conversion `json:"end_conversion"`
}

func ConvertRange(tb Timebase, start, end int64) (Range, error) {
	if start < 0 || end < start {
		return Range{}, fmt.Errorf("%w: invalid range [%d,%d)", ErrInvalidTimestamp, start, end)
	}
	cs, err := Convert(tb, start)
	if err != nil {
		return Range{}, err
	}
	ce, err := Convert(tb, end)
	if err != nil {
		return Range{}, err
	}
	return Range{
		StartTicks: cs.UnifiedTicks,
		EndTicks:   ce.UnifiedTicks,
		Timescale:  UnifiedTimescale,
		StartNS:    cs.Nanoseconds,
		EndNS:      ce.Nanoseconds,
		Start:      cs,
		End:        ce,
	}, nil
}

// MaxAbsTickError returns the larger endpoint rounding error in unified ticks.
func (r Range) MaxAbsTickError() float64 {
	return maxFloat(absFloat(r.Start.ErrorTicks), absFloat(r.End.ErrorTicks))
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
