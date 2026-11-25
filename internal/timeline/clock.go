// Package timeline establishes the explicit nanosecond time base shared by
// EBML blocks, fragments and RTP timestamps.
package timeline

import (
	"fmt"
	"math"

	"mediacheck/internal/report"
)

// MatroskaClock converts TimecodeScale-scaled block values to nanoseconds.
type MatroskaClock struct {
	// NanosecondsPerTick is Info/TimecodeScale (Matroska's default is 1ms).
	NanosecondsPerTick int64
	MaxSpanNS          int64
}

// NewMatroskaClock validates the explicit time base.
func NewMatroskaClock(timecodeScaleNS uint64, maxSpanNS int64) (*MatroskaClock, *report.Error) {
	if timecodeScaleNS == 0 || timecodeScaleNS > uint64(math.MaxInt64) {
		return nil, report.NewError(report.TimelineBadBase, "bad_timebase", "", 0, 0,
			"TimecodeScale must be in 1..%d nanoseconds", math.MaxInt64)
	}
	if maxSpanNS <= 0 {
		maxSpanNS = math.MaxInt64
	}
	return &MatroskaClock{NanosecondsPerTick: int64(timecodeScaleNS), MaxSpanNS: maxSpanNS}, nil
}

// BlockNS converts an absolute Cluster timecode plus signed block offset.
// offsets are raw input locations used when constructing the structured error.
func (c *MatroskaClock) BlockNS(clusterTimecode uint64, relativeTicks int16, elementOffset, failureOffset int) (int64, *report.Error) {
	if clusterTimecode > uint64(math.MaxInt64) {
		return 0, report.NewError(report.TimelineOverflow, "timestamp_overflow", "", elementOffset, failureOffset,
			"cluster timecode %d exceeds int64", clusterTimecode)
	}
	ticks := int64(clusterTimecode) + int64(relativeTicks)
	if ticks < 0 {
		return 0, report.NewError(report.TimelineOverflow, "negative_timestamp", "", elementOffset, failureOffset,
			"cluster timecode %d plus relative %d is negative", clusterTimecode, relativeTicks)
	}
	if ticks > math.MaxInt64/c.NanosecondsPerTick {
		return 0, report.NewError(report.TimelineOverflow, "timestamp_overflow", "", elementOffset, failureOffset,
			"timestamp ticks %d overflows nanosecond conversion", ticks)
	}
	ns := ticks * c.NanosecondsPerTick
	return ns, nil
}

// RTPDeltaNS converts an extended RTP tick difference to nanoseconds.
func RTPDeltaNS(deltaTicks int64, clockRate uint32) (int64, *report.Error) {
	if clockRate == 0 {
		return 0, report.NewError(report.TimelineBadBase, "bad_rtp_clock", "", 0, 0, "RTP clock rate is zero")
	}
	if deltaTicks < 0 {
		if deltaTicks == math.MinInt64 {
			return 0, report.NewError(report.TimelineOverflow, "timestamp_overflow", "", 0, 0, "RTP delta underflows")
		}
		ns, err := RTPDeltaNS(-deltaTicks, clockRate)
		if err != nil {
			return 0, err
		}
		return -ns, nil
	}
	// Convert quotient and remainder separately so delta*1e9 cannot overflow.
	seconds := deltaTicks / int64(clockRate)
	remainder := deltaTicks % int64(clockRate)
	if seconds > math.MaxInt64/1_000_000_000 {
		return 0, report.NewError(report.TimelineOverflow, "timestamp_overflow", "", 0, 0,
			"RTP delta %d at rate %d overflows nanoseconds", deltaTicks, clockRate)
	}
	return seconds*1_000_000_000 + remainder*1_000_000_000/int64(clockRate), nil
}

// Unwrapper expands bounded-width timestamp counters without guessing.
type Unwrapper struct {
	Bits         uint
	MaxJumpTicks int64
	modulus      int64
	last         int64
	haveLast     bool
}

// NewUnwrapper creates an unwrapper. bits is normally 32 for RTP.
func NewUnwrapper(bits uint, maxJumpTicks int64) (*Unwrapper, *report.Error) {
	if bits == 0 || bits > 32 {
		return nil, report.NewError(report.TimelineBadBase, "bad_wrap_width", "", 0, 0,
			"timestamp wrap width must be 1..32 bits, got %d", bits)
	}
	if maxJumpTicks < 0 {
		return nil, report.NewError(report.TimelineBadBase, "bad_wrap_bound", "", 0, 0,
			"max jump must be non-negative")
	}
	mod := int64(1) << bits
	if maxJumpTicks == 0 {
		maxJumpTicks = mod / 2
	}
	return &Unwrapper{Bits: bits, MaxJumpTicks: maxJumpTicks, modulus: mod}, nil
}

// Unwrap returns the unique expanded timestamp for raw. If zero or more than
// one modulo expansion lies inside the bounded jump window, it fails.
func (u *Unwrapper) Unwrap(raw uint64) (int64, *report.Error) {
	mask := uint64(u.modulus - 1)
	raw &= mask
	rawValue := int64(raw)
	if !u.haveLast {
		u.last = rawValue
		u.haveLast = true
		return u.last, nil
	}

	baseCycle := u.last / u.modulus
	var candidates []int64
	for cycle := baseCycle - 1; cycle <= baseCycle+1; cycle++ {
		value := rawValue + cycle*u.modulus
		distance := value - u.last
		if distance < 0 {
			distance = -distance
		}
		if distance <= u.MaxJumpTicks {
			candidates = append(candidates, value)
		}
	}
	if len(candidates) != 1 {
		return 0, report.NewError(report.TimelineWrapAmbiguous, "wrap_not_unique", "", 0, 0,
			"raw timestamp %d has %d expansions within %d ticks of %d",
			raw, len(candidates), u.MaxJumpTicks, u.last)
	}
	u.last = candidates[0]
	return u.last, nil
}

// TimeRange is an inclusive-start/exclusive-end nanosecond interval.
type TimeRange struct {
	StartNS int64 `json:"start_ns"`
	EndNS   int64 `json:"end_ns"`
}

// Duration returns end-start.
func (r TimeRange) Duration() int64 { return r.EndNS - r.StartNS }

// String makes diagnostic output stable.
func (r TimeRange) String() string {
	return fmt.Sprintf("[%d,%d)", r.StartNS, r.EndNS)
}
