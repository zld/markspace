// Package clock turns wrapping 32-bit RTP timestamps into a single monotonic
// media timeline expressed in integer ticks. All arithmetic is done with
// int64 and no floating point accumulation is used for time, so repeated
// runs over the same input produce identical timelines.
package clock

import (
	"fmt"
	"math"

	"example.com/mediaforge/internal/merrors"
)

// Timestamp modulus for RTP.
const Modulus = uint64(1) << 32

// maxWrapJump bounds how far a single packet may move the unwrapped clock.
// Anything larger is rejected instead of being folded into the timeline.
const maxWrapJump = 1 << 40

// Timeline converts 32-bit RTP timestamps for one stream into an extended,
// monotonically non-decreasing tick timeline.
type Timeline struct {
	clockRate uint32

	// lastRaw is the most recently accepted 32-bit timestamp.
	lastRaw  uint32
	lastExt  uint64
	hasLast  bool
	wraps    int
	backStep uint64 // furthest backwards jitter seen, in ticks

	// dependency of the extended clock on the arrival clock. A device whose
	// clock runs at driftPPM relative to the reference advances 1e6+driftPPM
	// ticks for every 1e6 reference ticks.
	driftPPM  int64
	refAtLast uint64
	hasRef    bool
}

// NewTimeline creates a timeline for a stream with the given clock rate.
func NewTimeline(clockRate uint32) (*Timeline, error) {
	if clockRate == 0 {
		return nil, merrors.New(merrors.StageTimeline, merrors.Invalid, "clock rate must be positive")
	}
	return &Timeline{clockRate: clockRate}, nil
}

// ClockRate returns the stream clock rate in ticks per second.
func (t *Timeline) ClockRate() uint32 { return t.clockRate }

// Wraps returns the number of 32-bit wrap-arounds observed.
func (t *Timeline) Wraps() int { return t.wraps }

// LastExtended returns the most recent extended timestamp and whether one
// exists.
func (t *Timeline) LastExtended() (uint64, bool) { return t.lastExt, t.hasLast }

// SignExtend interprets a raw 32-bit timestamp as the signed delta from a
// reference extended timestamp. It is exact for deltas within +-2^31.
func SignExtend(raw uint32, refExt uint64) uint64 {
	d := (int64(raw) - int64(uint32(refExt%Modulus)))
	return uint64(int64(refExt) + d)
}

// Extend maps a raw RTP timestamp onto the monotonic timeline. The first
// timestamp seeds the origin. Later timestamps are sign-extended relative to
// the last accepted value; a direct backward step of up to half the modulus
// is treated as reordering and does not move the committed clock.
//
// refTicks, when non-zero, is the arrival time on the shared reference clock
// and feeds device drift estimation. Pass 0 when no reference is available.
func (t *Timeline) Extend(raw uint32, refTicks uint64) (uint64, error) {
	if !t.hasLast {
		t.lastRaw = raw
		t.lastExt = uint64(raw)
		t.hasLast = true
		if refTicks != 0 {
			t.refAtLast = refTicks
			t.hasRef = true
		}
		return t.lastExt, nil
	}

	// Candidate extension assuming no wrap, then handle raw wrap-around.
	var ext uint64
	if raw >= t.lastRaw {
		ext = t.lastExt + uint64(raw-t.lastRaw)
	} else {
		t.wraps++
		ext = t.lastExt + uint64(Modulus-uint64(t.lastRaw)+uint64(raw))
	}

	if ext > t.lastExt {
		delta := ext - t.lastExt
		if delta > maxWrapJump {
			t.wraps-- // reject the wrap we speculatively counted
			return 0, merrors.New(merrors.StageTimeline, merrors.OutOfRange,
				"timestamp jump of %d ticks exceeds limit", delta)
		}
		if t.hasRef && refTicks >= t.refAtLast {
			refDelta := refTicks - t.refAtLast
			expected := refDelta
			if t.driftPPM != 0 {
				expected = refDelta + uint64(int64(refDelta)*t.driftPPM/1_000_000)
			}
			if expected > 0 && delta > expected+expected/4+uint64(t.clockRate)/50 {
				// A forward jump far beyond what the arrival clock supports
				// is a clock glitch: do not advance the committed timeline.
				t.wraps--
				return 0, merrors.New(merrors.StageTimeline, merrors.OutOfRange,
					"forward jump of %d ticks exceeds expected %d", delta, expected)
			}
		}
		t.lastExt = ext
	} else {
		// Reordered or duplicate timestamp; keep the committed clock, but
		// remember how far behind the stream is running.
		back := t.lastExt - ext
		if back > maxWrapJump {
			return 0, merrors.New(merrors.StageTimeline, merrors.OutOfRange,
				"timestamp rewind of %d ticks exceeds limit", back)
		}
		if back > t.backStep {
			t.backStep = back
		}
	}

	t.lastRaw = raw
	if refTicks != 0 {
		t.refAtLast = refTicks
		t.hasRef = true
	}
	return t.lastExt, nil
}

// SetDrift records a drift estimate in parts per million for the device
// clock relative to the reference clock. The estimate is only applied to
// future expectations; it never rewrites committed timestamps.
func (t *Timeline) SetDrift(ppm int64) error {
	if ppm < -100_000 || ppm > 100_000 {
		return merrors.New(merrors.StageTimeline, merrors.OutOfRange,
			"drift %d ppm outside +-100000", ppm)
	}
	t.driftPPM = ppm
	return nil
}

// DriftPPM returns the currently configured drift estimate.
func (t *Timeline) DriftPPM() int64 { return t.driftPPM }

// BackStep returns the largest backwards step observed, in ticks.
func (t *Timeline) BackStep() uint64 { return t.backStep }

// TicksToMicros converts a tick count at clockRate to integer microseconds
// using 128-bit-free exact division where possible.
func TicksToMicros(ticks uint64, clockRate uint32) uint64 {
	if clockRate == 0 {
		return 0
	}
	// ticks / rate seconds; micros = ticks * 1e6 / rate.
	if ticks <= math.MaxUint64/1_000_000 {
		return ticks * 1_000_000 / uint64(clockRate)
	}
	return ticks / uint64(clockRate) * 1_000_000
}

// MicrosToTicks converts integer microseconds to ticks at clockRate.
func MicrosToTicks(micros uint64, clockRate uint32) uint64 {
	return micros * uint64(clockRate) / 1_000_000
}

// String renders the timeline state for diagnostics.
func (t *Timeline) String() string {
	last := int64(-1)
	if t.hasLast {
		last = int64(t.lastExt)
	}
	return fmt.Sprintf("rate=%d last=%d wraps=%d drift=%dppm", t.clockRate, last, t.wraps, t.driftPPM)
}
