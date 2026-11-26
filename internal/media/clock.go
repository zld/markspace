package media

import (
	"fmt"
	"math/big"
)

// Rational is an exact rational number. D is always positive after construction.
type Rational struct {
	N int64 `json:"n"`
	D int64 `json:"d"`
}

var (
	Zero        = Rational{N: 0, D: 1}
	Second      = Rational{N: 1, D: 1}
	Millisecond = Rational{N: 1, D: 1000}
	Timebase48k = Rational{N: 1, D: 48000}
	Timebase90k = Rational{N: 1, D: 90000}
	Nanosecond  = Rational{N: 1, D: 1_000_000_000}
)

func Rat(n, d int64) (Rational, error) {
	if d == 0 {
		return Zero, SpecMismatch("CLOCK_BAD_RATIONAL", "rational denominator is zero")
	}
	if d < 0 {
		n, d = -n, -d
	}
	r := Rational{N: n, D: d}
	return r.normalize(), nil
}

func MustRat(n, d int64) Rational {
	r, err := Rat(n, d)
	if err != nil {
		panic(err)
	}
	return r
}

func (r Rational) normalize() Rational {
	if r.D < 0 {
		r.N, r.D = -r.N, -r.D
	}
	if r.D == 0 {
		return r
	}
	a := r.N
	if a < 0 {
		a = -a
	}
	b := r.D
	for b != 0 {
		a, b = b, a%b
	}
	if a > 1 {
		r.N /= a
		r.D /= a
	}
	return r
}

func (r Rational) IsZero() bool { return r.N == 0 }

func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.N, r.D)
}

func bigRat(r Rational) *big.Rat {
	return new(big.Rat).SetFrac64(r.N, r.D)
}

// Sub returns a-b without silently saturating.
func Sub(a, b Rational) Rational {
	x := new(big.Rat).Sub(bigRat(a), bigRat(b))
	return Rational{N: x.Num().Int64(), D: x.Denom().Int64()}
}

// Abs returns the absolute value.
func Abs(a Rational) Rational {
	if a.N < 0 {
		return Rational{N: -a.N, D: a.D}
	}
	return a
}

// ToTicks converts a duration expressed in seconds to an integer tick count
// in the requested per-tick time base. It rejects results that do not fit int64.
func ToTicks(duration, tickBase Rational, trackID, blockIndex int, offset int64) (int64, error) {
	if tickBase.D <= 0 || tickBase.N <= 0 {
		return 0, Corrupt("CLOCK_BAD_BASE", "invalid target time base").
			WithTrack(trackID, blockIndex).WithElement("", offset)
	}
	// ticks = duration / tickDuration = (N/D) * (tickBase.D/tickBase.N)
	num := new(big.Int).Mul(big.NewInt(duration.N), big.NewInt(tickBase.D))
	den := new(big.Int).Mul(big.NewInt(duration.D), big.NewInt(tickBase.N))
	if den.Sign() == 0 {
		return 0, Corrupt("CLOCK_BAD_BASE", "zero conversion denominator").
			WithTrack(trackID, blockIndex).WithElement("", offset)
	}
	q := new(big.Int).Quo(num, den)
	if !q.IsInt64() {
		return 0, Corrupt("CLOCK_OVERFLOW", "time conversion overflows int64 ticks").
			WithTrack(trackID, blockIndex).
			WithElement("", offset).
			WithDetail(fmt.Sprintf("duration=%s base=%s", duration, tickBase))
	}
	return q.Int64(), nil
}

// ToMilliseconds returns exact-ish milliseconds as float64 for reporting.
func ToMilliseconds(duration Rational) float64 {
	r, _ := new(big.Rat).Mul(bigRat(duration), big.NewRat(1000, 1)).Float64()
	return r
}

// ClockLocation identifies a block when a conversion cannot be performed.
type ClockLocation struct {
	TrackID    int
	BlockIndex int
	Offset     int64
}

// EventKind enumerates the unified timeline event families.
type EventKind string

const (
	EventAudio    EventKind = "audio"
	EventSubtitle EventKind = "subtitle"
	EventEffect   EventKind = "effect"
)

// TimeEvent is the format-independent timeline representation.
type TimeEvent struct {
	AssetID    string    `json:"asset_id"`
	TrackID    int       `json:"track_id"`
	BlockIndex int       `json:"block_index"`
	Kind       EventKind `json:"kind"`
	Start      Rational  `json:"start"`
	End        Rational  `json:"end"`
	Offset     int64     `json:"offset"`
	Payload    []byte    `json:"-"`
	CueID      string    `json:"cue_id,omitempty"`
	Text       string    `json:"text,omitempty"`
	EffectName string    `json:"effect_name,omitempty"`
}
