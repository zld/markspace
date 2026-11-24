// Package clock implements the shared rational media clock.
//
// Every subsystem converts container/RTP timestamps into the shared rate
// (1 tick = 1 nanosecond) while retaining the source scale and value. All
// arithmetic checks for overflow and never silently clamps: an out-of-range
// conversion returns a structured model error.
package clock

import (
	"math/big"

	"mediad/internal/model"
)

// Rate is a rational tick rate expressed as Num ticks per Den seconds.
// Examples: 90 kHz is {Num: 90000, Den: 1}; 44.1 kHz is {Num: 44100, Den: 1};
// a 29.97 fps video clock is {Num: 30000, Den: 1001}.
type Rate struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// Valid reports whether the rate is usable (both positive).
func (r Rate) Valid() bool { return r.Num > 0 && r.Den > 0 }

// Normal returns a reduced, canonical copy of the rate.
func (r Rate) Normal() Rate {
	if !r.Valid() {
		return r
	}
	g := new(big.Int).GCD(nil, nil, big.NewInt(r.Num), big.NewInt(r.Den))
	d := g.Int64()
	return Rate{Num: r.Num / d, Den: r.Den / d}
}

// Equal reports whether two rates describe the same frequency.
func (r Rate) Equal(o Rate) bool {
	if !r.Valid() || !o.Valid() {
		return false
	}
	// r.Num/r.Den == o.Num/o.Den without float conversion.
	return r.Num*o.Den == o.Num*r.Den
}

// SharedRate is the kernel-wide reference clock: 1 tick per nanosecond.
var SharedRate = Rate{Num: 1_000_000_000, Den: 1}

// Stamp is one timestamp with the rate it was authored in. Keeping the
// original value and scale means every output remains traceable to the wire.
type Stamp struct {
	Value int64 `json:"value"` // raw value in Source ticks
	Rate  Rate  `json:"rate"`  // ticks per second of the source
}

// Shared converts the stamp to the shared nanosecond clock.
//
// Rounding is fixed: nearest with halves rounded away from zero. Values that
// cannot be represented in int64 return model.ErrTimestampOverflow.
func (s Stamp) Shared() (int64, error) {
	if !s.Rate.Valid() {
		return 0, model.New(model.ErrTimestampOverflow, "invalid source rate num=%d den=%d", s.Rate.Num, s.Rate.Den)
	}
	return convert(s.Value, s.Rate, SharedRate)
}

// MustShared converts or panics; intended only for tests and fixtures.
func (s Stamp) MustShared() int64 {
	v, err := s.Shared()
	if err != nil {
		panic(err)
	}
	return v
}

// convert turns value ticks at from into ticks at to, using math/big so
// intermediate products cannot silently wrap.
func convert(value int64, from, to Rate) (int64, error) {
	num := big.NewInt(value)
	num.Mul(num, big.NewInt(to.Num))   // target ticks per second
	num.Mul(num, big.NewInt(from.Den)) // source seconds denominator
	den := big.NewInt(from.Num)
	den.Mul(den, big.NewInt(to.Den))

	// Round half away from zero, deterministically, without sign branching bugs.
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(num, den, rem)
	twoRem := new(big.Int).Abs(rem)
	twoRem.Mul(twoRem, big.NewInt(2))
	if twoRem.Cmp(den) >= 0 {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, model.New(model.ErrTimestampOverflow,
			"value %d @ %d/%d overflows shared clock", value, from.Num, from.Den)
	}
	return q.Int64(), nil
}

// FromShared maps a shared nanosecond value onto the given source rate with
// the same half-away-from-zero rounding rule.
func FromShared(shared int64, target Rate) (int64, error) {
	if !target.Valid() {
		return 0, model.New(model.ErrTimestampOverflow, "invalid target rate num=%d den=%d", target.Num, target.Den)
	}
	return convert(shared, SharedRate, target)
}

// CheckMonotonic validates an ordered DTS sequence in the shared clock.
// Equal timestamps are allowed (B-frame reorder can share a decode instant),
// any decrease is reported with the offending sample index in the message.
func CheckMonotonic(shared []int64) error {
	for i := 1; i < len(shared); i++ {
		if shared[i] < shared[i-1] {
			return model.New(model.ErrNonMonotonicDTS,
				"DTS went backwards at sample %d: %d < %d", i, shared[i], shared[i-1])
		}
	}
	return nil
}

// CheckTrackBases requires every track to declare the same normalized rate.
// The 1-based track index of the first offender is included in the error.
func CheckTrackBases(rates []Rate) error {
	if len(rates) == 0 {
		return nil
	}
	base := rates[0].Normal()
	for i, r := range rates {
		if !r.Valid() {
			return model.New(model.ErrTimebaseMismatch, "track %d declares invalid rate %+v", i+1, r)
		}
		if !r.Equal(base) {
			return model.New(model.ErrTimebaseMismatch,
				"track %d rate %d/%d differs from track 1 rate %d/%d",
				i+1, r.Num, r.Den, base.Num, base.Den).AtTrack(i + 1)
		}
	}
	return nil
}
