// Package model holds the shared, serializable primitives used by every
// subsystem: the nanosecond time base, frame metadata and structured faults.
package model

import (
	"fmt"
	"math"
)

// Timestamp is a point on the project-wide nanosecond time base.
type Timestamp int64

// MaxTimestamp is the exclusive upper bound of the valid time base
// (roughly year 2262). Values at or above it are rejected.
const MaxTimestamp = int64(math.MaxInt64)

// Validate rejects negative and overflowing timestamps. This is the single
// gate used by ingest, timeline, planner and render before arithmetic.
func (t Timestamp) Validate() error {
	if t < 0 {
		return NewFault(KindInput, CodeTimeInvalid, 0, 0, fmt.Sprintf("timestamp %d ns is negative", int64(t)))
	}
	return nil
}

// Duration validates a non-negative span. Zero is allowed and means the
// caller derives the duration from neighbouring frames.
func Duration(ns int64) error {
	if ns < 0 {
		return NewFault(KindInput, CodeTimeInvalid, 0, 0, fmt.Sprintf("duration %d ns is negative", ns))
	}
	return nil
}

// AddNS adds two nanosecond values without wrapping. The result must stay on
// the valid time base; overflow is reported, never silently truncated.
func AddNS(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, NewFault(KindInput, CodeTimeOverflow, 0, 0,
			fmt.Sprintf("timestamp addition %d + %d ns overflows int64", a, b))
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, NewFault(KindInput, CodeTimeUnderflow, 0, 0,
			fmt.Sprintf("timestamp addition %d + %d ns underflows int64", a, b))
	}
	sum := a + b
	if sum < 0 {
		return 0, NewFault(KindInput, CodeTimeInvalid, 0, 0,
			fmt.Sprintf("timestamp addition %d + %d ns is negative", a, b))
	}
	return sum, nil
}

// SubNS subtracts timestamps and rejects negative or overflowing spans.
func SubNS(a, b int64) (int64, error) {
	if b > 0 && a < math.MinInt64+b {
		return 0, NewFault(KindInput, CodeTimeUnderflow, 0, 0,
			fmt.Sprintf("timestamp subtraction %d - %d ns underflows int64", a, b))
	}
	if b < 0 && a > math.MaxInt64+b {
		return 0, NewFault(KindInput, CodeTimeOverflow, 0, 0,
			fmt.Sprintf("timestamp subtraction %d - %d ns overflows int64", a, b))
	}
	diff := a - b
	if diff < 0 {
		return 0, NewFault(KindInput, CodeTimeInvalid, 0, 0,
			fmt.Sprintf("timestamp subtraction %d - %d ns is negative", a, b))
	}
	return diff, nil
}

// Clock anchors a stream on the shared nanosecond time base. Every incoming
// event is rebased through the clock so cross-track comparison is exact.
type Clock struct {
	// EpochNS is the wall-clock origin of media time zero.
	EpochNS int64
}

// NewClock validates the epoch and returns the shared clock.
func NewClock(epochNS int64) (*Clock, error) {
	if err := Timestamp(epochNS).Validate(); err != nil {
		return nil, err
	}
	return &Clock{EpochNS: epochNS}, nil
}

// Rebase converts a stream-local timestamp to the project time base.
func (c *Clock) Rebase(localNS, offsetNS int64) (Timestamp, error) {
	if err := Timestamp(localNS).Validate(); err != nil {
		return 0, err
	}
	if err := Duration(offsetNS); err != nil {
		return 0, err
	}
	sum, err := AddNS(c.EpochNS, localNS)
	if err != nil {
		return 0, err
	}
	sum, err = AddNS(sum, offsetNS)
	if err != nil {
		return 0, err
	}
	if err := Timestamp(sum).Validate(); err != nil {
		return 0, err
	}
	return Timestamp(sum), nil
}

// Offset returns the distance of t from the clock epoch.
func (c *Clock) Offset(t Timestamp) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, err
	}
	if int64(t) < c.EpochNS {
		return 0, NewFault(KindInput, CodeTimeRegression, 0, 0,
			fmt.Sprintf("timestamp %d ns precedes clock epoch %d ns", int64(t), c.EpochNS))
	}
	return int64(t) - c.EpochNS, nil
}

// RTPToNS converts a 32-bit RTP timestamp at the given clock rate to
// nanoseconds without intermediate 64-bit overflow.
func RTPToNS(rtp uint32, clockRate uint32) (int64, error) {
	if clockRate == 0 {
		return 0, NewFault(KindInput, CodeTimeInvalid, 0, 0, "RTP clock rate is zero")
	}
	const nsPerSecond = int64(1_000_000_000)
	whole := int64(rtp) / int64(clockRate)
	rem := int64(rtp) % int64(clockRate)
	if whole > MaxTimestamp/nsPerSecond {
		return 0, NewFault(KindInput, CodeTimeOverflow, 0, 0,
			fmt.Sprintf("RTP timestamp %d at %d Hz exceeds nanosecond range", rtp, clockRate))
	}
	base := whole * nsPerSecond
	fraction := rem * nsPerSecond / int64(clockRate)
	if base > MaxTimestamp-fraction {
		return 0, NewFault(KindInput, CodeTimeOverflow, 0, 0,
			fmt.Sprintf("RTP timestamp %d at %d Hz exceeds nanosecond range", rtp, clockRate))
	}
	return base + fraction, nil
}
