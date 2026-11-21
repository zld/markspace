// Package clock expands the 33-bit, 90 kHz PTS/DTS timestamps used by
// MPEG-TS into a monotonic 64-bit time line, classifying every jump as a
// normal increment, a legal modulo-2^33 wrap, or a stream restart.
package clock

import "time"

const (
	// Mod is the modulus of a 33-bit MPEG timestamp: 2^33.
	Mod uint64 = 1 << 33
	// Half is half the modulus; jumps are classified against it.
	Half uint64 = Mod / 2
	// TicksPerSecond is the 90 kHz MPEG time base.
	TicksPerSecond uint64 = 90_000
)

// JumpKind classifies the relation between one timestamp and the previous one.
type JumpKind int

const (
	// JumpNone marks the first timestamp fed to the unwrapper.
	JumpNone JumpKind = iota
	// JumpNormal is a non-negative forward increment within the modulus.
	JumpNormal
	// JumpWrap is a legal wrap: the raw value went backwards by more than
	// half the modulus because the 33-bit counter overflowed.
	JumpWrap
	// JumpRestart is a backwards jump of at most half the modulus, which
	// cannot be a legal wrap and therefore indicates a stream restart.
	JumpRestart
)

// String renders the jump kind for diagnostics.
func (k JumpKind) String() string {
	switch k {
	case JumpNone:
		return "none"
	case JumpNormal:
		return "normal"
	case JumpWrap:
		return "wrap"
	case JumpRestart:
		return "restart"
	default:
		return "unknown"
	}
}

// Unwrapper expands wrapping 33-bit timestamps into a monotonic 64-bit line.
// The zero value is ready to use.
type Unwrapper struct {
	lastRaw  uint64
	lastExp  uint64
	has      bool
	restarts int
	wraps    int
}

// Feed consumes one raw 33-bit timestamp (values are masked to 33 bits) and
// returns the expanded timestamp together with the classified jump kind.
func (u *Unwrapper) Feed(raw uint64) (uint64, JumpKind) {
	raw &= Mod - 1
	if !u.has {
		u.has = true
		u.lastRaw = raw
		u.lastExp = raw
		return raw, JumpNone
	}

	var expanded uint64
	var kind JumpKind
	switch {
	case raw >= u.lastRaw:
		// Ordinary forward step.
		expanded = u.lastExp + (raw - u.lastRaw)
		kind = JumpNormal
	case u.lastRaw-raw > Half:
		// Backwards by more than half the modulus: legal counter overflow.
		expanded = u.lastExp + (Mod - u.lastRaw) + raw
		kind = JumpWrap
		u.wraps++
	default:
		// Small backwards jump: the encoder clock restarted. Re-base the
		// expanded line onto the new raw value instead of going backwards.
		expanded = raw
		kind = JumpRestart
		u.restarts++
	}
	u.lastRaw = raw
	u.lastExp = expanded
	return expanded, kind
}

// Has reports whether Feed has been called at least once.
func (u *Unwrapper) Has() bool { return u.has }

// Last returns the most recent expanded timestamp.
func (u *Unwrapper) Last() uint64 { return u.lastExp }

// Wraps returns the number of legal modulo wraps observed so far.
func (u *Unwrapper) Wraps() int { return u.wraps }

// Restarts returns the number of backwards (restart) jumps observed so far.
func (u *Unwrapper) Restarts() int { return u.restarts }

// Reset returns the unwrapper to its zero state.
func (u *Unwrapper) Reset() { *u = Unwrapper{} }

// Duration converts a 90 kHz tick count to a time.Duration.
func Duration(ticks uint64) time.Duration {
	secs := ticks / TicksPerSecond
	rem := ticks % TicksPerSecond
	return time.Duration(secs)*time.Second +
		time.Duration(rem)*time.Second/time.Duration(TicksPerSecond)
}

// Milliseconds converts a 90 kHz tick count to whole milliseconds.
func Milliseconds(ticks uint64) int64 {
	return int64(ticks * 1000 / TicksPerSecond)
}
