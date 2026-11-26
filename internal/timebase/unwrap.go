package timebase

import "errors"

// ErrAmbiguousJump is returned when a raw counter jumps by exactly half
// the modulus, so the wrap direction cannot be decided.
var ErrAmbiguousJump = errors.New("ambiguous jump: delta equals half modulus")

// Unwrapper expands a wrapped counter (PTS 33-bit, RTP 32-bit, ...) to a
// monotonic 64-bit scale by tracking wrap cycles. Values must be fed in
// media order, not arrival order.
type Unwrapper struct {
	modulus int64
	half    int64
	last    int64
	cycles  int64
	init    bool
	seed    int64
	seeded  bool
}

// NewUnwrapper creates an Unwrapper for the given modulus (e.g. 1<<33).
func NewUnwrapper(modulus int64) *Unwrapper {
	return &Unwrapper{modulus: modulus, half: modulus / 2}
}

// Seed pins the first unwrapped value near ref (epoch alignment between
// counters that share a clock but use different moduli).
func (u *Unwrapper) Seed(ref int64) {
	if !u.init {
		u.seed = ref
		u.seeded = true
	}
}

// Unwrap expands one raw value. It returns ErrAmbiguousJump when the
// delta from the previous value is exactly half the modulus.
func (u *Unwrapper) Unwrap(v int64) (int64, error) {
	if !u.init {
		if u.seeded {
			u.cycles = roundDiv(u.seed-v, u.modulus)
		}
		u.init = true
		u.last = v
		return v + u.cycles*u.modulus, nil
	}
	d := v - u.last
	switch {
	case d > u.half:
		u.cycles--
	case d < -u.half:
		u.cycles++
	case d == u.half || d == -u.half:
		if u.modulus%2 == 0 {
			return 0, ErrAmbiguousJump
		}
	}
	u.last = v
	return v + u.cycles*u.modulus, nil
}

// roundDiv returns round(a/b) with halves away from zero; b must be > 0.
func roundDiv(a, b int64) int64 {
	q := a / b // truncates toward zero
	r := a % b // same sign as a
	if 2*abs64(r) >= b {
		if a >= 0 {
			q++
		} else {
			q--
		}
	}
	return q
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
