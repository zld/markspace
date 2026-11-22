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
	lastRaw   uint32
	lastExt   uint64
	originRaw uint64
	hasLast   bool
	backStep  uint64 // furthest backwards jitter seen, in ticks

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

// Wraps returns the number of 32-bit wrap-arounds crossed by the committed
// timeline. It is derived from the total advance, so reordering or rejected
// jumps cannot inflate it.
func (t *Timeline) Wraps() int {
	if !t.hasLast {
		return 0
	}
	base := t.originRaw / Modulus
	return int(t.lastExt/Modulus - base)
}

// LastExtended returns the most recent extended timestamp and whether one
// exists.
func (t *Timeline) LastExtended() (uint64, bool) { return t.lastExt, t.hasLast }

// maxForwardStep bounds a single accepted forward advance of the raw clock.
// At 90 kHz this is roughly three hours between consecutive packets, so a
// legitimate stream never reaches it but a corrupt timestamp does.
const maxForwardStep = int64(1) << 30

// SignExtend maps raw onto the 32-bit epoch closest to refExt, using a
// signed 32-bit delta. It is exact for deltas within +-2^31.
func SignExtend(raw uint32, refExt uint64) uint64 {
	d := int64(int32(raw - uint32(refExt%Modulus)))
	return uint64(int64(refExt) + d)
}

// Extend maps a raw RTP timestamp onto the monotonic timeline. The first
// timestamp seeds the origin. Later timestamps are sign-extended relative to
// the last accepted value; a backward step of up to half the modulus is
// treated as reordering and does not move the committed clock.
//
// refTicks, when non-zero, is the arrival time on the shared reference clock
// and feeds device drift estimation. Pass 0 when no reference is available.
func (t *Timeline) Extend(raw uint32, refTicks uint64) (uint64, error) {
	if !t.hasLast {
		t.lastRaw = raw
		t.lastExt = uint64(raw)
		t.originRaw = uint64(raw)
		t.hasLast = true
		if refTicks != 0 {
			t.refAtLast = refTicks
			t.hasRef = true
		}
		return t.lastExt, nil
	}

	// Signed 32-bit delta handles ordinary advancement, reordering, and
	// wrap-around in one operation.
	delta := int64(int32(raw - t.lastRaw))

	if delta > 0 {
		if delta > maxForwardStep {
			return 0, merrors.New(merrors.StageTimeline, merrors.OutOfRange,
				"forward step of %d ticks exceeds limit", delta)
		}
		if t.hasRef && refTicks >= t.refAtLast {
			refDelta := refTicks - t.refAtLast
			expected := refDelta
			if t.driftPPM != 0 {
				expected = refDelta + uint64(int64(refDelta)*t.driftPPM/1_000_000)
			}
			// Tolerance must not scale with the arrival delta: several
			// packets routinely arrive back to back (a burst), each advancing
			// the media clock by a full packet interval. It is instead a
			// fixed slack plus a generous fraction of the expected delta.
			// 250 ms of media is beyond any plausible queueing delay and far
			// below a genuine clock glitch.
			tolerance := expected/2 + uint64(t.clockRate)/4 + 1
			if uint64(delta) > expected+tolerance {
				// A forward jump far beyond what the arrival clock supports
				// is a clock glitch: do not advance the committed timeline.
				return 0, merrors.New(merrors.StageTimeline, merrors.OutOfRange,
					"forward jump of %d ticks exceeds expected %d", delta, expected)
			}
		}
		t.lastExt = uint64(int64(t.lastExt) + delta)
		t.lastRaw = raw
	} else if delta < 0 {
		back := uint64(-delta)
		if back > maxWrapJump {
			return 0, merrors.New(merrors.StageTimeline, merrors.OutOfRange,
				"timestamp rewind of %d ticks exceeds limit", back)
		}
		if back > t.backStep {
			t.backStep = back
		}
		// lastRaw stays at the high-water mark so that a reordered packet
		// cannot shift the mapping of later packets.
	}

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
	return fmt.Sprintf("rate=%d last=%d wraps=%d drift=%dppm", t.clockRate, last, t.Wraps(), t.driftPPM)
}
