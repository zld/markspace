// Package clock unifies PCR/PTS unwrapping and provides the time base and
// error-event model shared by every processing module.
package clock

// Mod33 is the modulus of the 33-bit timestamps carried by MPEG-TS
// (PCR_base and PTS/DTS are both 33-bit fields ticking at 90 kHz).
const Mod33 uint64 = 1 << 33

// HalfMod33 is the decision threshold. A raw delta whose absolute value is
// greater than half the modulus cannot be distinguished from a jump in the
// opposite direction without extra context.
const HalfMod33 int64 = int64(Mod33 / 2)

// UnwrapResult is the outcome of feeding one raw timestamp value.
type UnwrapResult struct {
	// Value is the monotonically unwrapped timestamp in 90 kHz units.
	Value uint64
	// Wrapped reports that a modulo-2^33 wraparound was resolved on this call.
	Wrapped bool
	// Ambiguous reports that the raw jump is larger than half the modulus:
	// the caller must record an error event instead of trusting Value.
	Ambiguous bool
}

// Unwrapper33 unwraps a stream of 33-bit timestamps into a monotonic 64-bit
// counter. It is not safe for concurrent use.
type Unwrapper33 struct {
	initialized bool
	last        uint64
}

// NewUnwrapper33 returns a ready-to-use unwrapper.
func NewUnwrapper33() *Unwrapper33 { return &Unwrapper33{} }

// Last returns the most recent unwrapped value and whether the unwrapper has
// been fed at least once.
func (u *Unwrapper33) Last() (uint64, bool) { return u.last, u.initialized }

// Reset clears all state.
func (u *Unwrapper33) Reset() { u.initialized, u.last = false, 0 }

// Feed consumes one raw 33-bit timestamp and returns its unwrapped form.
// Values larger than Mod33-1 are rejected as ambiguous input.
func (u *Unwrapper33) Feed(raw uint64) UnwrapResult {
	if raw >= Mod33 {
		return UnwrapResult{Value: u.last, Ambiguous: true}
	}
	if !u.initialized {
		u.initialized = true
		u.last = raw
		return UnwrapResult{Value: raw}
	}
	delta := int64(raw) - int64(u.last%Mod33)
	switch {
	case delta == 0:
		// Repeated timestamp: keep the established timeline.
		return UnwrapResult{Value: u.last}
	case delta < -HalfMod33:
		// Backward move beyond half the modulus means the counter wrapped.
		value := u.last + uint64(delta+int64(Mod33))
		u.last = value
		return UnwrapResult{Value: value, Wrapped: true}
	case delta > HalfMod33:
		// Forward move beyond half the modulus is undecidable: it may be a
		// backwards jump or an implausibly large forward one.
		return UnwrapResult{Value: u.last, Ambiguous: true}
	default:
		value := u.last + uint64(delta)
		u.last = value
		return UnwrapResult{Value: value}
	}
}
