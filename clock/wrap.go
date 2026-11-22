// Package clock turns raw, possibly wrapping or resetting, sender clocks into
// a monotonic expanded time base and estimates linear drift between tracks.
package clock

import (
	"errors"
)

// Reason explains why an unwrapper produced a given output.
type Reason uint8

const (
	ReasonInit   Reason = iota // first observed value
	ReasonNormal               // ordinary forward advance
	ReasonWrap                 // modular counter carried (16/32-bit wrap)
	ReasonLate                 // backward move within the reorder threshold
	ReasonReset                // backward move beyond the threshold: clock restart
	ReasonJump                 // forward move beyond the threshold: discontinuity
)

func (r Reason) String() string {
	switch r {
	case ReasonInit:
		return "init"
	case ReasonNormal:
		return "normal"
	case ReasonWrap:
		return "wrap"
	case ReasonLate:
		return "late"
	case ReasonReset:
		return "reset"
	case ReasonJump:
		return "jump"
	default:
		return "unknown"
	}
}

// Unwrapper expands modular counters (RTP 16-bit sequence numbers and 32-bit
// timestamps) into int64 space, detects sender clock resets and keeps a
// restorable baseline so checkpoints can resume deterministically.
type Unwrapper struct {
	bits          uint
	mod           uint64
	half          int64
	lateThreshold uint64 // modular distance tolerated as reordering
	jumpThreshold uint64 // forward distance treated as a discontinuity
	initialized   bool
	lastRaw       uint64
	lastExpanded  int64
	wraps         uint64
	resets        uint64
}

// NewUnwrapper builds an unwrapper. bits is the counter width (16 or 32 for
// RTP); lateThreshold bounds in-window reordering, jumpThreshold bounds
// plausible forward increments; both are measured in raw counter units.
func NewUnwrapper(bits uint, lateThreshold, jumpThreshold uint64) (*Unwrapper, error) {
	if bits < 8 || bits > 63 {
		return nil, errors.New("clock: unwrapper bit width must be in [8,63]")
	}
	if lateThreshold == 0 || jumpThreshold <= lateThreshold {
		return nil, errors.New("clock: require 0 < lateThreshold < jumpThreshold")
	}
	mod := uint64(1) << bits
	return &Unwrapper{
		bits: bits, mod: mod, half: int64(mod / 2),
		lateThreshold: lateThreshold, jumpThreshold: jumpThreshold,
	}, nil
}

// NewTimestampUnwrapper presets RTP timestamp thresholds from the clock rate:
// one second of reorder tolerance and sixty seconds of jump tolerance.
func NewTimestampUnwrapper(rate uint32) (*Unwrapper, error) {
	return NewUnwrapper(32, uint64(rate), uint64(rate)*60)
}

// NewSequenceUnwrapper presets the 512-packet reorder window mandated by the
// kernel limits.
func NewSequenceUnwrapper() (*Unwrapper, error) {
	return NewUnwrapper(16, 512, 30000)
}

// Unwrap maps a raw modular value onto the expanded axis. The output never
// decreases after a reset: a reset rebuilds the baseline immediately after
// the previously committed value and is reported via the reason.
func (u *Unwrapper) Unwrap(value uint64) (int64, Reason) {
	value %= u.mod
	if !u.initialized {
		u.initialized = true
		u.lastRaw, u.lastExpanded = value, int64(value)
		return u.lastExpanded, ReasonInit
	}
	fwd := (value - u.lastRaw) % u.mod
	delta := int64(fwd)
	if delta >= u.half {
		delta -= int64(u.mod) // signed modular distance
	}
	switch {
	case delta > 0 && value < u.lastRaw:
		// forward modular carry: the counter wrapped through zero
		u.wraps++
		u.lastRaw, u.lastExpanded = value, u.lastExpanded+delta
		return u.lastExpanded, ReasonWrap
	case delta < 0 && uint64(-delta) > u.lateThreshold:
		// backwards beyond the reorder window: sender clock restarted
		u.resets++
		u.lastRaw, u.lastExpanded = value, u.lastExpanded+1
		return u.lastExpanded, ReasonReset
	case delta >= 0 && fwd > u.jumpThreshold:
		u.resets++
		u.lastRaw, u.lastExpanded = value, u.lastExpanded+1
		return u.lastExpanded, ReasonJump
	default:
		reason := ReasonNormal
		if delta < 0 {
			reason = ReasonLate
		}
		u.lastRaw, u.lastExpanded = value, u.lastExpanded+delta
		return u.lastExpanded, reason
	}
}

// Wraps reports the number of modular carries observed.
func (u *Unwrapper) Wraps() uint64 { return u.wraps }

// Resets reports the number of baseline rebuilds (resets and jumps).
func (u *Unwrapper) Resets() uint64 { return u.resets }

// LastExpanded returns the most recent expanded value.
func (u *Unwrapper) LastExpanded() int64 { return u.lastExpanded }

// UnwrapState is the serialisable checkpoint of an unwrapper.
type UnwrapState struct {
	Bits          uint   `json:"bits"`
	LastRaw       uint64 `json:"last_raw"`
	LastExpanded  int64  `json:"last_expanded"`
	Wraps         uint64 `json:"wraps"`
	Resets        uint64 `json:"resets"`
	LateThreshold uint64 `json:"late_threshold"`
	JumpThreshold uint64 `json:"jump_threshold"`
	Initialized   bool   `json:"initialized"`
}

// Snapshot captures the full unwrapper state for checkpointing.
func (u *Unwrapper) Snapshot() UnwrapState {
	return UnwrapState{
		Bits: u.bits, LastRaw: u.lastRaw, LastExpanded: u.lastExpanded,
		Wraps: u.wraps, Resets: u.resets,
		LateThreshold: u.lateThreshold, JumpThreshold: u.jumpThreshold,
		Initialized: u.initialized,
	}
}

// RestoreUnwrap rebuilds an unwrapper from a checkpoint snapshot.
func RestoreUnwrap(s UnwrapState) (*Unwrapper, error) {
	u, err := NewUnwrapper(s.Bits, s.LateThreshold, s.JumpThreshold)
	if err != nil {
		return nil, err
	}
	u.initialized, u.lastRaw, u.lastExpanded = s.Initialized, s.LastRaw, s.LastExpanded
	u.wraps, u.resets = s.Wraps, s.Resets
	return u, nil
}
