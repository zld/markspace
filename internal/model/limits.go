package model

import "math"

// Default limits guard against unbounded memory growth. They are deliberately
// conservative for an offline kernel and can be overridden through pipeline
// parameters; any override is itself bounded by Max*.
const (
	// DefaultMaxFileBytes caps a single input container/event file at 1 GiB.
	DefaultMaxFileBytes int64 = 1 << 30
	// MaxFileBytes is the hard ceiling regardless of configuration.
	MaxFileBytes int64 = 4 << 30
	// DefaultMaxReorderWindow is the default RTP reorder window in packets.
	DefaultMaxReorderWindow = 256
	// MaxReorderWindow is the hard ceiling for the reorder window.
	MaxReorderWindow = 8192
	// DefaultMaxTrackSamples caps samples materialized per track.
	DefaultMaxTrackSamples = 1_000_000
	// MaxTrackSamples is the hard ceiling.
	MaxTrackSamples = 16_000_000
)

// Limits groups the configurable resource bounds for one pipeline run.
type Limits struct {
	MaxFileBytes     int64 `json:"max_file_bytes"`
	MaxReorderWindow int   `json:"max_reorder_window"`
	MaxTrackSamples  int   `json:"max_track_samples"`
}

// DefaultLimits returns the standard bounded configuration.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:     DefaultMaxFileBytes,
		MaxReorderWindow: DefaultMaxReorderWindow,
		MaxTrackSamples:  DefaultMaxTrackSamples,
	}
}

// Validate rejects out-of-range limits with a structured error.
func (l Limits) Validate() error {
	if l.MaxFileBytes <= 0 || l.MaxFileBytes > MaxFileBytes {
		return NewError(ClassLimitExceeded, "max_file_bytes=%d outside (0,%d]", l.MaxFileBytes, MaxFileBytes)
	}
	if l.MaxReorderWindow <= 0 || l.MaxReorderWindow > MaxReorderWindow {
		return NewError(ClassLimitExceeded, "max_reorder_window=%d outside (0,%d]", l.MaxReorderWindow, MaxReorderWindow)
	}
	if l.MaxTrackSamples <= 0 || l.MaxTrackSamples > MaxTrackSamples {
		return NewError(ClassLimitExceeded, "max_track_samples=%d outside (0,%d]", l.MaxTrackSamples, MaxTrackSamples)
	}
	return nil
}

// ToTicks converts a duration expressed in a track-local time scale into the
// canonical integer nanosecond tick base. A zero/overflow time scale and
// results outside int64 are rejected rather than silently clamped.
func ToTicks(trackTime int64, timescale uint32) (Ticks, error) {
	if timescale == 0 {
		return 0, NewError(ClassInvalidTimebase, "zero timescale for track time %d", trackTime)
	}
	if trackTime < 0 {
		return 0, NewError(ClassOutOfRange, "negative track time %d", trackTime)
	}
	// numerator may reach ~9.2e18 * 1e9, so detect overflow explicitly.
	if trackTime > math.MaxInt64/int64(Nano) {
		return 0, NewError(ClassOutOfRange, "track time %d overflows nanosecond base", trackTime)
	}
	num := trackTime * int64(Nano)
	return Ticks(num / int64(timescale)), nil
}

// DurationTicks converts (count, timescale) the same way as ToTicks.
func DurationTicks(count, timescale uint64) (Ticks, error) {
	if timescale == 0 {
		return 0, NewError(ClassInvalidTimebase, "zero timescale for duration count %d", count)
	}
	if count > uint64(math.MaxInt64)/uint64(Nano) {
		return 0, NewError(ClassOutOfRange, "duration count %d overflows nanosecond base", count)
	}
	return Ticks(int64(count*uint64(Nano)) / int64(timescale)), nil
}

// Millis renders Ticks as a floating millisecond value for human reports.
func (t Ticks) Millis() float64 {
	return float64(t) / float64(Milli)
}
