package media

import (
	"errors"
	"fmt"
	"math"
)

// Rational is an immutable numerator/denominator media time base.
// Denominator is always positive after construction.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), den)
	if g > 0 {
		num /= g
		den /= g
	}
	return Rational{Num: num, Den: den}, nil
}

func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
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

// Seconds converts a tick count to floating-point seconds.
func (r Rational) Seconds(ticks int64) float64 {
	if r.Den == 0 {
		return 0
	}
	return float64(ticks) * float64(r.Num) / float64(r.Den)
}

// Microseconds converts ticks to integer microseconds using rounded division.
func (r Rational) Microseconds(ticks int64) int64 {
	if r.Den == 0 || r.Num == 0 {
		return 0
	}
	num := ticks * r.Num * 1_000_000
	den := r.Den
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// TicksFromMicroseconds maps microseconds to the nearest tick count.
func (r Rational) TicksFromMicroseconds(us int64) int64 {
	if r.Num == 0 {
		return 0
	}
	num := us * r.Den
	den := r.Num * 1_000_000
	if den < 0 {
		num, den = -num, -den
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// FrameDuration returns the rational duration of one frame at rate fps.
func FrameDuration(fps Rational) Rational {
	return Rational{Num: fps.Den, Den: fps.Num}
}

// ExpectedTicks returns ticks for frameIndex (zero based) at fps in tb.
func ExpectedTicks(frameIndex int, fps, tb Rational) int64 {
	// seconds = index * fps.Den / fps.Num
	// ticks = seconds * tb.Den / tb.Num
	n := int64(frameIndex) * fps.Den * tb.Den
	d := fps.Num * tb.Num
	if d == 0 {
		return 0
	}
	if n >= 0 {
		return (n + d/2) / d
	}
	return -((-n + d/2) / d)
}

// ExpectedMicroseconds returns the rounded repair time for frameIndex.
func ExpectedMicroseconds(frameIndex int, fps Rational) int64 {
	d := FrameDuration(fps)
	us := int64(frameIndex) * d.Num * 1_000_000 / d.Den
	rem := int64(frameIndex) * d.Num * 1_000_000 % d.Den
	if rem*2 >= d.Den {
		us++
	}
	return us
}

func MaxAbsInt64(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}

func ApproxEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}
