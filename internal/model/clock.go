// Package model defines the shared media time base and error model used by
// every stage of the mediasync kernel.
package model

import (
	"fmt"
	"math"
)

// Nanos is a signed timestamp on the kernel's single time base. One tick is
// one nanosecond. Signed values are used so edit lists and negative offsets
// can be represented while being validated.
type Nanos int64

const (
	// NanosPerSecond is the number of kernel time-base ticks in one second.
	NanosPerSecond int64 = 1_000_000_000
	// NanosPerMilli is the number of kernel time-base ticks in one millisecond.
	NanosPerMilli int64 = 1_000_000
)

// ToNanos converts a value expressed in the given media timescale to the
// kernel time base. The result is rounded to the nearest nanosecond using
// integer arithmetic, which keeps conversions deterministic on every
// platform.
func ToNanos(value int64, timescale uint32) (Nanos, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("timescale must be positive")
	}
	// Split the multiplication so a full int64 timestamp can never overflow
	// while still rounding correctly.
	whole := value / int64(timescale)
	rem := value % int64(timescale)
	wholeNs := whole * NanosPerSecond
	if whole != 0 && (wholeNs/NanosPerSecond != whole) {
		return 0, fmt.Errorf("timestamp %d in timescale %d overflows nanosecond time base", value, timescale)
	}
	remNs := (rem*NanosPerSecond + int64(timescale)/2) / int64(timescale)
	// remNs has the sign of rem; the two terms cannot both overflow because
	// wholeNs is bounded by the check above and remNs is below one second.
	if (remNs > 0 && wholeNs > math.MaxInt64-remNs) || (remNs < 0 && wholeNs < math.MinInt64-remNs) {
		return 0, fmt.Errorf("timestamp %d in timescale %d overflows nanosecond time base", value, timescale)
	}
	return Nanos(wholeNs + remNs), nil
}

// NanosToScale converts a kernel timestamp back into the given media
// timescale, truncating toward zero.
func NanosToScale(ts Nanos, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("timescale must be positive")
	}
	return int64(ts) / NanosPerSecond * int64(timescale), nil
}

// MS converts a duration on the kernel time base into milliseconds. It is a
// convenience for machine-readable summaries.
func (n Nanos) MS() float64 {
	return float64(n) / float64(NanosPerMilli)
}

// Interval is a half-open media-time interval [Start, End) on the kernel
// time base.
type Interval struct {
	Start Nanos
	End   Nanos
}

// Valid reports whether the interval is non-empty and ordered.
func (i Interval) Valid() bool { return i.End > i.Start }

// String formats an interval deterministically.
func (i Interval) String() string {
	return fmt.Sprintf("[%d,%d)", i.Start, i.End)
}

// TrackKind identifies the media family. It is intentionally codec agnostic:
// the kernel moves opaque packets and never decodes payloads.
type TrackKind string

const (
	KindVideo TrackKind = "video"
	KindAudio TrackKind = "audio"
)

// Valid reports whether the kind is known.
func (k TrackKind) Valid() bool {
	return k == KindVideo || k == KindAudio
}
