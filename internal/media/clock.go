package media

import (
	"errors"
	"math"
)

// TicksPerSecond is the common integer timeline scale. It is divisible by
// 48 kHz Opus, 44.1 kHz AAC and millisecond subtitle time bases.
const TicksPerSecond int64 = 7_056_000

const (
	OpusRateHz int64 = 48_000
	AACRateHz  int64 = 44_100
)

var (
	OpusScale = TicksPerSecond / OpusRateHz // 147 ticks per Opus sample
	AACScale  = TicksPerSecond / AACRateHz  // 160 ticks per AAC sample
	MSScale   = TicksPerSecond / 1_000      // 7,056 ticks per millisecond
)

// CheckedMul multiplies two non-negative int64 values and reports overflow.
func CheckedMul(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, errors.New("clock: negative time value")
	}
	if b != 0 && a > math.MaxInt64/b {
		return 0, errors.New("clock: multiplication overflow")
	}
	return a * b, nil
}

// CheckedAdd adds two non-negative offsets and reports int64 overflow.
func CheckedAdd(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, errors.New("clock: negative time value")
	}
	if a > math.MaxInt64-b {
		return 0, errors.New("clock: addition overflow")
	}
	return a + b, nil
}

// FromRate converts a value in rateHz units to the common tick scale.
func FromRate(value, rateHz int64) (int64, error) {
	if rateHz <= 0 {
		return 0, errors.New("clock: non-positive time base")
	}
	scale := TicksPerSecond / rateHz
	if scale*rateHz != TicksPerSecond {
		// The selected fixtures use exact divisors. Keep arbitrary rates exact
		// by division after the checked product.
		v, err := CheckedMul(value, TicksPerSecond)
		if err != nil {
			return 0, err
		}
		return v / rateHz, nil
	}
	return CheckedMul(value, scale)
}

func FromOpusSamples(samples uint32) (int64, error) {
	return CheckedMul(int64(samples), OpusScale)
}

func FromAACSamples(samples uint32) (int64, error) {
	return CheckedMul(int64(samples), AACScale)
}

func FromMilliseconds(ms int64) (int64, error) {
	return CheckedMul(ms, MSScale)
}

func ToMilliseconds(ticks int64) int64 {
	return ticks / MSScale
}
