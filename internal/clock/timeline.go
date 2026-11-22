// Package clock provides the integer media timebase shared by the transport,
// container and metadata subsystems. All conversions are exact rational
// operations with explicit overflow checks.
package clock

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// UnifiedTimescale is the tick rate of the platform-wide media timebase.
// 90000 is the classic MPEG clock and converts exactly for the 90 kHz video,
// 48 kHz audio and 1 kHz metadata timescales used by the platform.
const UnifiedTimescale uint32 = 90000

var (
	// ErrZeroTimescale reports a conversion using a zero or negative clock rate.
	ErrZeroTimescale = errors.New("clock: timescale must be positive")
	// ErrOverflow reports an arithmetic result that does not fit in int64.
	ErrOverflow = errors.New("clock: arithmetic overflow")
	// ErrDurationOverflow reports media that runs past its container duration.
	ErrDurationOverflow = errors.New("clock: media exceeds declared container duration")
)

// ConversionError describes a failed exact timebase conversion.
type ConversionError struct {
	Value    int64
	From, To uint32
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("clock: convert %d ticks from %d Hz to %d Hz: %v", e.Value, e.From, e.To, ErrOverflow)
}

// Unwrap allows errors.Is(err, ErrOverflow).
func (e *ConversionError) Unwrap() error { return ErrOverflow }

func magnitude(value int64) uint64 {
	if value < 0 {
		return uint64(-(value + 1)) + 1
	}
	return uint64(value)
}

func signed(q uint64, negative bool) (int64, error) {
	if negative {
		if q > uint64(math.MaxInt64)+1 {
			return 0, ErrOverflow
		}
		if q == uint64(math.MaxInt64)+1 {
			return math.MinInt64, nil
		}
		return -int64(q), nil
	}
	if q > uint64(math.MaxInt64) {
		return 0, ErrOverflow
	}
	return int64(q), nil
}

// Convert rescales value from the from timescale to the to timescale, rounding
// halves away from zero. It never silently wraps.
func Convert(value int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, fmt.Errorf("clock: convert %d: %w", value, ErrZeroTimescale)
	}
	if from == to {
		return value, nil
	}
	hi, lo := bits.Mul64(magnitude(value), uint64(to))
	if hi >= uint64(from) {
		return 0, &ConversionError{Value: value, From: from, To: to}
	}
	q, r := bits.Div64(hi, lo, uint64(from))
	if r*2 >= uint64(from) {
		q++
	}
	out, err := signed(q, value < 0)
	if err != nil {
		return 0, &ConversionError{Value: value, From: from, To: to}
	}
	return out, nil
}

// scaleInt computes value*num/den with half-away-from-zero rounding.
func scaleInt(value, num, den int64) (int64, error) {
	if den <= 0 || num < 0 {
		return 0, fmt.Errorf("clock: scale by %d/%d: %w", num, den, ErrOverflow)
	}
	hi, lo := bits.Mul64(magnitude(value), uint64(num))
	if hi >= uint64(den) {
		return 0, ErrOverflow
	}
	q, r := bits.Div64(hi, lo, uint64(den))
	if r*2 >= uint64(den) {
		q++
	}
	return signed(q, value < 0)
}

// Unwrapper folds 32-bit RTP timestamps into a monotonic int64 domain. A real
// wrap produces a positive 32-bit delta; a negative delta is reported as a
// backwards (non-monotonic) timestamp, never hidden.
type Unwrapper struct {
	last     uint32
	ext      int64
	seen     bool
	wraps    uint64
	backward uint64
}

// Unwrapped is the result of pushing one raw timestamp.
type Unwrapped struct {
	Extended  int64
	Delta     int32
	Wrapped   bool
	Backwards bool
}

// Push folds ts into the extended timeline.
func (u *Unwrapper) Push(ts uint32) Unwrapped {
	if !u.seen {
		u.seen = true
		u.last = ts
		u.ext = int64(ts)
		return Unwrapped{Extended: u.ext}
	}
	delta := int32(ts - u.last) // two's-complement handles the 2^32 wrap
	wrapped := delta > 0 && u.last > 0xE0000000 && ts < 0x20000000
	backwards := delta < 0
	u.ext += int64(delta)
	u.last = ts
	if wrapped {
		u.wraps++
	}
	if backwards {
		u.backward++
	}
	return Unwrapped{Extended: u.ext, Delta: delta, Wrapped: wrapped, Backwards: backwards}
}

// Extended returns the current extended timestamp.
func (u *Unwrapper) Extended() int64 { return u.ext }

// Stats returns wrap and backwards counters.
func (u *Unwrapper) Stats() (wraps, backwards uint64) { return u.wraps, u.backward }

// Mapped is one timestamp mapped onto the unified 90 kHz media line.
type Mapped struct {
	RTP          uint32
	Extended     int64
	Unified      int64
	DeltaUnified int64
	Wrapped      bool
	Backwards    bool
}

// TimelineStats are observational counters for one track line.
type TimelineStats struct {
	Samples      uint64
	Wraps        uint64
	NonMonotonic uint64
}

// Timeline maps one track's RTP clock onto the unified media line. DriftPPM is
// the measured or configured rate error of the sender clock in parts per
// million (positive means the sender runs fast). Mapping is deterministic.
type Timeline struct {
	trackID      uint32
	timescale    uint32
	unw          Unwrapper
	epochExt     int64
	epochUnified int64
	driftPPM     int64
	lastUnified  int64
	hasLast      bool
	stats        TimelineStats
}

// NewTimeline creates a mapping anchored at epochRTP/epochUnified. Drift must
// be within +/-100000 ppm.
func NewTimeline(trackID, timescale uint32, epochRTP uint32, epochUnified, driftPPM int64) (*Timeline, error) {
	if timescale == 0 {
		return nil, fmt.Errorf("clock: track %d: %w", trackID, ErrZeroTimescale)
	}
	if driftPPM < -100000 || driftPPM > 100000 {
		return nil, fmt.Errorf("clock: track %d: drift %d ppm out of range", trackID, driftPPM)
	}
	t := &Timeline{trackID: trackID, timescale: timescale, epochUnified: epochUnified, driftPPM: driftPPM}
	t.epochExt = t.unw.Push(epochRTP).Extended
	return t, nil
}

// TrackID returns the owning track.
func (t *Timeline) TrackID() uint32 { return t.trackID }

// Timescale returns the source RTP clock rate.
func (t *Timeline) Timescale() uint32 { return t.timescale }

// Epoch returns the unified epoch tick.
func (t *Timeline) Epoch() int64 { return t.epochUnified }

// Map converts a raw RTP timestamp to the unified timeline. Backwards motion is
// flagged in the result but never clamped or fabricated.
func (t *Timeline) Map(rtp uint32) (Mapped, error) {
	u := t.unw.Push(rtp)
	delta := u.Extended - t.epochExt
	converted, err := Convert(delta, t.timescale, UnifiedTimescale)
	if err != nil {
		return Mapped{}, fmt.Errorf("clock: track %d: %w", t.trackID, err)
	}
	if t.driftPPM != 0 {
		converted, err = scaleInt(converted, 1000000+t.driftPPM, 1000000)
		if err != nil {
			return Mapped{}, fmt.Errorf("clock: track %d drift: %w", t.trackID, err)
		}
	}
	unified, ok := addInt64(t.epochUnified, converted)
	if !ok {
		return Mapped{}, fmt.Errorf("clock: track %d unified time: %w", t.trackID, ErrOverflow)
	}
	m := Mapped{RTP: rtp, Extended: u.Extended, Unified: unified, DeltaUnified: converted, Wrapped: u.Wrapped, Backwards: u.Backwards}
	t.stats.Samples++
	t.stats.Wraps += boolToUint(u.Wrapped)
	if t.hasLast && unified < t.lastUnified {
		m.Backwards = true
	}
	if m.Backwards {
		t.stats.NonMonotonic++
	}
	t.lastUnified = unified
	t.hasLast = true
	return m, nil
}

// Stats returns a snapshot of timeline counters.
func (t *Timeline) Stats() TimelineStats { return t.stats }

func boolToUint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func addInt64(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, false
	}
	return a + b, true
}

// DurationError describes media that extends beyond the container declaration.
type DurationError struct {
	Declared  uint64
	Actual    uint64
	Timescale uint32
}

func (e *DurationError) Error() string {
	return fmt.Sprintf("clock: media ends at %d ticks but container declares %d ticks at %d Hz: %v",
		e.Actual, e.Declared, e.Timescale, ErrDurationOverflow)
}

// Unwrap allows errors.Is(err, ErrDurationOverflow).
func (e *DurationError) Unwrap() error { return ErrDurationOverflow }

// CheckDeclaredDuration verifies that media ending at actualEnd (track ticks)
// does not run past the duration declaration. A zero timescale is rejected.
func CheckDeclaredDuration(declared uint64, timescale uint32, actualEnd int64) error {
	if timescale == 0 {
		return ErrZeroTimescale
	}
	if actualEnd < 0 {
		return fmt.Errorf("clock: negative media end %d", actualEnd)
	}
	if uint64(actualEnd) > declared {
		return &DurationError{Declared: declared, Actual: uint64(actualEnd), Timescale: timescale}
	}
	return nil
}
