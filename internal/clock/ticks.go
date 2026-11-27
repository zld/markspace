// Package clock defines the single integer media time base used across the
// regression harness and the modular-arithmetic helpers that expand wrapped
// transport timestamps into monotonically increasing tick counts.
package clock

import "errors"

// MasterHz is the common media scale every track is converted into. It is
// chosen to be an integer multiple of the common RTP video clock (90 kHz), so
// converting RTP timestamps loses no precision.
const MasterHz int64 = 90000

// Tick is a point or duration on the master integer media time line.
type Tick int64

// ErrInvalidTimeBase is returned when a track declares a zero or otherwise
// unusable timescale, making conversion to master ticks impossible.
var ErrInvalidTimeBase = errors.New("invalid media time base")

// Convert turns a value expressed in srcHz units into master ticks using
// nearest rounding. It returns ErrInvalidTimeBase when srcHz is not positive.
// The result is deterministic for identical (value, srcHz) inputs.
func Convert(value int64, srcHz int64) (Tick, error) {
	if srcHz <= 0 {
		return 0, ErrInvalidTimeBase
	}
	num := value * MasterHz
	if num >= 0 {
		return Tick((num + srcHz/2) / srcHz), nil
	}
	return Tick(-((-num + srcHz/2) / srcHz)), nil
}

// WrapModulus is the modular range of the transport counters handled here:
// RTP timestamps and RTP sequence numbers are both unsigned 32/16 bit fields.
type WrapModulus uint64

const (
	// RTPTimestampMod is 2^32, the RTP timestamp wrap modulus.
	RTPTimestampMod WrapModulus = 1 << 32
	// RTPSequenceMod is 2^16, the RTP sequence number wrap modulus.
	RTPSequenceMod WrapModulus = 1 << 16
)

// Unwrap expands a modular counter value into an absolute count, choosing the
// representative of value's congruence class that lies nearest to the
// previous absolute value. prev must be an already established absolute
// value; to seed a stream use the first raw value verbatim (see UnwrapMany).
// modulus must be a power of two and greater than one.
func Unwrap(prev int64, value uint64, modulus WrapModulus) int64 {
	m := int64(modulus)
	half := m / 2
	cur := int64(value)
	// Number of whole modulus periods already accumulated.
	periods := prev / m
	guess := periods*m + cur
	for guess-prev > half {
		guess -= m
	}
	for prev-guess > half {
		guess += m
	}
	return guess
}

// UnwrapMany unwraps a stream of modular values: the first raw value seeds
// the absolute time line verbatim, and every later value is expanded against
// the running previous value.
func UnwrapMany(values []uint64, modulus WrapModulus) []int64 {
	if len(values) == 0 {
		return nil
	}
	out := make([]int64, len(values))
	out[0] = int64(values[0])
	for i := 1; i < len(values); i++ {
		out[i] = Unwrap(out[i-1], values[i], modulus)
	}
	return out
}
