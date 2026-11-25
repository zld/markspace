// Package clock defines the shared rational time base used by ingest,
// indexing, timeline merging, rendering, and verification.
package clock

import (
	"fmt"
	"math/big"
)

// NanosecondsPerSecond is the target base for all merged timelines.
const NanosecondsPerSecond int64 = 1_000_000_000

// Timebase is the rational size of one timestamp tick: Numerator/Denominator
// seconds. For example, RTP 90 kHz is {1, 90000}.
type Timebase struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

var (
	// Nanosecond is the canonical timeline time base.
	Nanosecond = Timebase{Numerator: 1, Denominator: NanosecondsPerSecond}
	// RTP90K is the common video RTP clock.
	RTP90K = Timebase{Numerator: 1, Denominator: 90_000}
	// PTS48K is a common audio/presentation clock used in fixtures.
	PTS48K = Timebase{Numerator: 1, Denominator: 48_000}
)

// Validate reports whether the rational time base is usable.
func (tb Timebase) Validate() error {
	if tb.Numerator <= 0 {
		return fmt.Errorf("timebase numerator must be positive, got %d", tb.Numerator)
	}
	if tb.Denominator <= 0 {
		return fmt.Errorf("timebase denominator must be positive, got %d", tb.Denominator)
	}
	return nil
}

// ToNanoseconds converts ts ticks in tb to integer nanoseconds. The conversion
// uses arbitrary-precision intermediates so a malicious or unusual timestamp
// cannot trigger int64 multiplication overflow. Fractional nanoseconds are
// truncated toward zero.
func (tb Timebase) ToNanoseconds(ts int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}

	value := new(big.Int).Mul(big.NewInt(ts), big.NewInt(tb.Numerator))
	value.Mul(value, big.NewInt(NanosecondsPerSecond))
	value.Quo(value, big.NewInt(tb.Denominator))
	if !value.IsInt64() {
		return 0, fmt.Errorf("timestamp %d in timebase %d/%d overflows nanoseconds", ts, tb.Numerator, tb.Denominator)
	}
	return value.Int64(), nil
}

// MustNanos is intended for tests and fixed fixtures only.
func MustNanos(ts int64, tb Timebase) int64 {
	ns, err := tb.ToNanoseconds(ts)
	if err != nil {
		panic(err)
	}
	return ns
}
