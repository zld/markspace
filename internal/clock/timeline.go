// Package clock defines the unified integer time scale shared by every
// subsystem. All MXF/MP4/RTP time stamps are converted into ticks on this
// master scale, so alignment logic never mixes floating point seconds or
// heterogeneous track time bases.
package clock

import "errors"

// MasterTickRate is the number of integer ticks per second on the master
// timeline. 90 kHz divides the common media rates (24/25/30/48 fps,
// 48/44.1 kHz audio, 90 kHz RTP video) with small or zero remainder while
// keeping every value an integer.
const MasterTickRate int64 = 90000

// ErrInvalidRate is returned when a conversion is requested with a zero or
// negative source rate, which can never produce a meaningful time stamp.
var ErrInvalidRate = errors.New("clock: source time scale must be positive")

// Scale converts value ticks expressed at srcRate ticks/second onto the
// master integer scale. Conversion is exact where the rates divide evenly
// and rounded to nearest (half away from zero) otherwise. Negative inputs
// are supported so signed offsets convert correctly.
func Scale(value, srcRate int64) (int64, error) {
	if srcRate <= 0 {
		return 0, ErrInvalidRate
	}
	return scale(value, srcRate, MasterTickRate), nil
}

// ScaleTo converts between two arbitrary integer rates. It is used when a
// source format must be expressed on a track-local scale rather than the
// master scale.
func ScaleTo(value, srcRate, dstRate int64) (int64, error) {
	if srcRate <= 0 || dstRate <= 0 {
		return 0, ErrInvalidRate
	}
	return scale(value, srcRate, dstRate), nil
}

// FromMaster expresses master ticks on another integer scale.
func FromMaster(masterTicks, dstRate int64) (int64, error) {
	if dstRate <= 0 {
		return 0, ErrInvalidRate
	}
	return scale(masterTicks, MasterTickRate, dstRate), nil
}

// Ticks returns the whole-tick duration of the given number of milliseconds
// on the master scale.
func Ticks(millis int64) int64 {
	return millis * MasterTickRate / 1000
}

// Micros converts a master-tick duration into microseconds.
func Micros(masterTicks int64) int64 {
	return masterTicks * 1_000_000 / MasterTickRate
}

// Millis converts a master-tick duration into milliseconds.
func Millis(masterTicks int64) int64 {
	return masterTicks * 1000 / MasterTickRate
}

func scale(value, srcRate, dstRate int64) int64 {
	num := value * dstRate
	// Round half away from zero without relying on floating point math.
	if num >= 0 {
		return (num + srcRate/2) / srcRate
	}
	return -((-num + srcRate/2) / srcRate)
}
