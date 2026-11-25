// Package clock provides integer media time units and conversions.
//
// All time values are represented by signed integer ticks plus an explicit
// timescale (ticks per second). Conversions never clamp out-of-range values.
package clock

import (
	"errors"
	"fmt"
	"math"
)

// Stage is reused by packages that need to identify where a time error arose.
type Stage string

const (
	StageAssemble Stage = "assemble"
)

var (
	ErrInvalidScale = errors.New("clock: timescale must be positive")
	ErrInvalidRange = errors.New("clock: invalid time range")
	ErrOverflow     = errors.New("clock: time conversion overflow")
)

// Time is an integer tick position in the supplied timescale.
type Time struct {
	Ticks     int64  `json:"ticks"`
	Timescale uint32 `json:"timescale"`
}

func NewTime(ticks int64, timescale uint32) (Time, error) {
	if timescale == 0 {
		return Time{}, ErrInvalidScale
	}
	return Time{Ticks: ticks, Timescale: timescale}, nil
}

// Milliseconds returns an exact integer millisecond value when possible.
func (t Time) Milliseconds() (int64, error) {
	return Convert(t.Ticks, t.Timescale, 1000)
}

// Range is a half-open [Start, End) interval in one timescale.
type Range struct {
	Start     int64  `json:"start_ticks"`
	End       int64  `json:"end_ticks"`
	Timescale uint32 `json:"timescale"`
}

func NewRange(start, end int64, timescale uint32) (Range, error) {
	if timescale == 0 {
		return Range{}, fmt.Errorf("%w: timescale", ErrInvalidScale)
	}
	if start < 0 || end < start {
		return Range{}, fmt.Errorf("%w: [%d,%d) at %d", ErrInvalidRange, start, end, timescale)
	}
	return Range{Start: start, End: end, Timescale: timescale}, nil
}

func (r Range) Duration() int64 { return r.End - r.Start }

func (r Range) IsZero() bool {
	return r.Timescale == 0 && r.Start == 0 && r.End == 0
}

// Milliseconds returns the half-open range in 1000-based ticks.
func (r Range) Milliseconds() (Range, error) {
	start, err := Convert(r.Start, r.Timescale, 1000)
	if err != nil {
		return Range{}, err
	}
	end, err := Convert(r.End, r.Timescale, 1000)
	if err != nil {
		return Range{}, err
	}
	return Range{Start: start, End: end, Timescale: 1000}, nil
}

// Convert converts ticks from one timescale to another using nearest integer.
// It returns an error instead of silently truncating when multiplication
// would overflow.
func Convert(ticks int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, ErrInvalidScale
	}
	if from == to || ticks == 0 {
		return ticks, nil
	}

	// For the common exact divisibility case, divide first to reduce magnitude.
	if uint64(from)%uint64(to) == 0 {
		div := int64(from / to)
		if ticks%div == 0 {
			return ticks / div, nil
		}
	}
	if uint64(to)%uint64(from) == 0 {
		mul := int64(to / from)
		if ticks > math.MaxInt64/mul || ticks < math.MinInt64/mul {
			return 0, fmt.Errorf("%w: %d*%d", ErrOverflow, ticks, mul)
		}
		return ticks * mul, nil
	}

	// Round to nearest: ticks*to/from, with half away from zero.
	num := bigMul(ticks, int64(to))
	if num.overflow {
		return 0, fmt.Errorf("%w: %d*%d", ErrOverflow, ticks, to)
	}
	den := int64(from)
	q, rem := num.value/den, num.value%den
	if rem != 0 {
		if rem*2 >= den {
			q++
		} else if rem*2 <= -den {
			q--
		}
	}
	return q, nil
}

type mulResult struct {
	value    int64
	overflow bool
}

func bigMul(a, b int64) mulResult {
	if a == 0 || b == 0 {
		return mulResult{}
	}
	if a > math.MaxInt64/b || a < math.MinInt64/b {
		return mulResult{overflow: true}
	}
	return mulResult{value: a * b}
}

// ConvertRange converts both endpoints to another timescale.
func ConvertRange(r Range, to uint32) (Range, error) {
	start, err := Convert(r.Start, r.Timescale, to)
	if err != nil {
		return Range{}, err
	}
	end, err := Convert(r.End, r.Timescale, to)
	if err != nil {
		return Range{}, err
	}
	return NewRange(start, end, to)
}

// OverlapAtScale returns the intersection in the requested timescale.
func OverlapAtScale(a, b Range, timescale uint32) (Range, bool, error) {
	ar, err := ConvertRange(a, timescale)
	if err != nil {
		return Range{}, false, err
	}
	br, err := ConvertRange(b, timescale)
	if err != nil {
		return Range{}, false, err
	}
	start := max64(ar.Start, br.Start)
	end := min64(ar.End, br.End)
	if end <= start {
		return Range{}, false, nil
	}
	out, err := NewRange(start, end, timescale)
	return out, err == nil, err
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
