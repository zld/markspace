// Package core holds the primitives shared by every medtrace subsystem:
// an integer-nanosecond time base, track epochs and source-positioned faults.
package core

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Nanos is an integer number of nanoseconds. Every timestamp that crosses a
// package boundary is expressed in this unit so that comparisons and hashes
// remain deterministic across platforms.
type Nanos int64

const (
	// Second is one second in nanoseconds.
	Second Nanos = 1_000_000_000
	// Millisecond is one millisecond in nanoseconds.
	Millisecond Nanos = 1_000_000
)

// String renders a duration as an exact decimal number of seconds.
func (n Nanos) String() string {
	sign := ""
	v := int64(n)
	if v < 0 {
		sign = "-"
		if v == -1<<63 {
			return "-9223372036.854775808s"
		}
		v = -v
	}
	sec := v / int64(Second)
	frac := v % int64(Second)
	if frac == 0 {
		return sign + strconv.FormatInt(sec, 10) + "s"
	}
	fs := strconv.FormatInt(frac, 10)
	fs = strings.Repeat("0", 9-len(fs)) + fs
	fs = strings.TrimRight(fs, "0")
	return sign + strconv.FormatInt(sec, 10) + "." + fs + "s"
}

// Seconds converts to a float; only for display, never for decisions.
func (n Nanos) Seconds() float64 { return float64(n) / float64(Second) }

// FromMillis converts whole milliseconds to nanoseconds.
func FromMillis(ms int64) Nanos { return Nanos(ms) * Millisecond }

// FromTicks converts media ticks to integer nanoseconds using exact integer
// arithmetic, rounding down (towards negative infinity). A zero timescale is
// rejected.
func FromTicks(ticks int64, timescale uint32) (Nanos, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("timescale must be positive")
	}
	num := big.NewInt(ticks)
	num.Mul(num, big.NewInt(int64(Second)))
	den := big.NewInt(int64(timescale))
	// big.Int Div does Euclidean division: the remainder is always >= 0, so the
	// quotient is the floor for negative numerators as required.
	q, _ := new(big.Int).DivMod(num, den, new(big.Int))
	if !q.IsInt64() {
		return 0, fmt.Errorf("tick value %d at timescale %d overflows nanoseconds", ticks, timescale)
	}
	return Nanos(q.Int64()), nil
}

// TicksToNanos is FromTicks without error handling, for call sites that have
// already validated the timescale; it panics on overflow.
func TicksToNanos(ticks int64, timescale uint32) Nanos {
	n, err := FromTicks(ticks, timescale)
	if err != nil {
		panic(err)
	}
	return n
}

// Scale converts an interval measured at one timescale into another, rounding
// down. It is used to translate sample durations without float arithmetic.
func Scale(ticks int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, fmt.Errorf("timescale must be positive (from=%d to=%d)", from, to)
	}
	num := big.NewInt(ticks)
	num.Mul(num, big.NewInt(int64(to)))
	q, _ := new(big.Int).DivMod(num, big.NewInt(int64(from)), new(big.Int))
	if !q.IsInt64() {
		return 0, fmt.Errorf("scaled value overflows int64")
	}
	return q.Int64(), nil
}

// TrackKind distinguishes the media types medtrace understands.
type TrackKind string

const (
	Video TrackKind = "video"
	Audio TrackKind = "audio"
	Other TrackKind = "other"
)

// Track identifies a media track. Representation is empty for single-rate
// manifests and carries the DASH representation ID when present.
type Track struct {
	Kind           TrackKind
	ID             uint32
	Representation string
}

// Key returns a stable string form suitable for maps and report ordering.
func (t Track) Key() string {
	rep := t.Representation
	if rep == "" {
		rep = "-"
	}
	return fmt.Sprintf("%s/%s/%d", t.Kind, rep, t.ID)
}

// Epoch is a run of media whose timestamps are expressed against one linear
// timeline. A new epoch starts whenever the source timeline jumps: a DASH
// representation switch, an HLS discontinuity, an RTP timestamp wrap or a
// backwards step. Every sample receives a canonical Nanos value that is
// continuous across epochs, plus the raw source tick and the epoch number that
// produced it.
type Epoch struct {
	// Number is the zero-based epoch counter within a run.
	Number int
	// Reason records why the epoch was opened.
	Reason EpochReason
	// Source names the manifest entry that began the epoch (segment URI or
	// event anchor).
	Source string
	// RawBase is the first raw timestamp observed in this epoch, in the
	// track's own unit (media ticks for fMP4, RTP units for events).
	RawBase int64
	// CanonicalBase is the canonical nanosecond position assigned to RawBase.
	CanonicalBase Nanos
	// Delta is CanonicalBase minus the raw-to-nanos mapping of RawBase. It is
	// the constant added to every converted timestamp in the epoch so the
	// canonical timeline stays monotonic.
	Delta Nanos
	// BoundaryOffset is the change in Delta relative to the previous epoch.
	BoundaryOffset Nanos
}

// EpochReason enumerates the discontinuities that open a new epoch.
type EpochReason string

const (
	EpochStart          EpochReason = "start"
	EpochRepresentation EpochReason = "representation-switch"
	EpochDiscontinuity  EpochReason = "hls-discontinuity"
	EpochWrap           EpochReason = "timestamp-wrap"
	EpochBackwards      EpochReason = "timestamp-backwards"
	EpochManifestOrder  EpochReason = "manifest-order"
)

// Valid reports whether the reason is one this package defines.
func (r EpochReason) Valid() bool {
	switch r {
	case EpochStart, EpochRepresentation, EpochDiscontinuity, EpochWrap, EpochBackwards, EpochManifestOrder:
		return true
	}
	return false
}

// fmtSigned formats a signed nanosecond delta for reports.
func fmtSigned(n Nanos) string {
	if n > 0 {
		return "+" + n.String()
	}
	return n.String()
}

// EpochTracker assigns canonical nanoseconds to raw timestamps and keeps the
// canonical timeline monotonic across discontinuities. Scanning and streaming
// both drive the same tracker, so they classify jumps identically.
type EpochTracker struct {
	epochs     []Epoch
	current    Epoch
	active     bool
	lastCanon  Nanos
	lastRaw    int64
	haveRaw    bool
	rawUnit    string
	wrapPeriod int64
}

// NewEpochTracker creates a tracker for a raw unit ("ticks" or "rtp"). For
// RTP, wrapPeriod is 1<<32; for media ticks pass 0 to disable wrap detection
// (representations may legitimately jump forward a lot).
func NewEpochTracker(rawUnit string, wrapPeriod int64) *EpochTracker {
	return &EpochTracker{rawUnit: rawUnit, wrapPeriod: wrapPeriod}
}

// Epochs returns a copy of the epochs opened so far, in order.
func (t *EpochTracker) Epochs() []Epoch {
	return append([]Epoch(nil), t.epochs...)
}

// Current returns the active epoch.
func (t *EpochTracker) Current() Epoch { return t.current }

// Last returns the greatest canonical position emitted so far.
func (t *EpochTracker) Last() Nanos { return t.lastCanon }

// OpenStart begins the initial epoch at canonical zero.
func (t *EpochTracker) OpenStart(source string, raw int64, rawNanos Nanos) Nanos {
	t.open(EpochStart, source, raw, rawNanos, 0)
	return t.current.CanonicalBase
}

// OpenAfter ends the current epoch and starts a new one whose first raw value
// maps to lastCanon. boundary is the canonical position at which the new
// source begins; when the caller has no better estimate it should pass
// t.Last(). Because the new epoch's base is anchored at boundary, the delta
// jump is measured against the previous epoch and reported in BoundaryOffset.
func (t *EpochTracker) OpenAfter(reason EpochReason, source string, raw int64, rawNanos, boundary Nanos) Nanos {
	t.open(reason, source, raw, rawNanos, boundary)
	return t.current.CanonicalBase
}

func (t *EpochTracker) open(reason EpochReason, source string, raw int64, rawNanos, canonicalBase Nanos) {
	prevDelta := Nanos(0)
	if t.active {
		prevDelta = t.current.Delta
	}
	delta := canonicalBase - rawNanos
	ep := Epoch{
		Number:         len(t.epochs),
		Reason:         reason,
		Source:         source,
		RawBase:        raw,
		CanonicalBase:  canonicalBase,
		Delta:          delta,
		BoundaryOffset: delta - prevDelta,
	}
	t.epochs = append(t.epochs, ep)
	t.current = ep
	t.active = true
	t.lastRaw = raw
	t.haveRaw = true
	if canonicalBase > t.lastCanon {
		t.lastCanon = canonicalBase
	}
}

// Jump inspects a raw timestamp arriving after prevRaw in the current epoch
// and reports the epoch reason it implies, or "" when it is continuous.
// Backwards means strictly less than the previous raw value; Wrap means the
// value fell by more than WrapThreshold when the unit is a wrapping counter.
func (t *EpochTracker) Jump(prevRaw, raw int64) EpochReason {
	if raw >= prevRaw {
		return ""
	}
	if t.wrapPeriod > 0 && prevRaw-raw > t.wrapPeriod/2 {
		return EpochWrap
	}
	if raw < prevRaw {
		return EpochBackwards
	}
	return ""
}

// Map converts a raw timestamp already known to be continuous in the current
// epoch. The result is clamped to lastCanon so canonical time never regresses.
func (t *EpochTracker) Map(rawNanos Nanos) (Nanos, bool) {
	pos := rawNanos + t.current.Delta
	if t.active && pos < t.lastCanon {
		return t.lastCanon, false
	}
	t.lastCanon = pos
	return pos, true
}

// NoteRaw advances the tracker's remembered raw value.
func (t *EpochTracker) NoteRaw(raw int64) { t.lastRaw, t.haveRaw = raw, true }

// LastRaw reports the most recent raw timestamp fed to the tracker.
func (t *EpochTracker) LastRaw() (int64, bool) { return t.lastRaw, t.haveRaw }

// ToTicks converts nanoseconds to media ticks at timescale, rounding down.
// It is the inverse of FromTicks and is used when a duration declared in
// nanoseconds must be expressed in a template's tick unit.
func ToTicks(n Nanos, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("timescale must be positive")
	}
	num := big.NewInt(int64(n))
	num.Mul(num, big.NewInt(int64(timescale)))
	q, _ := new(big.Int).DivMod(num, big.NewInt(int64(Second)), new(big.Int))
	if !q.IsInt64() {
		return 0, fmt.Errorf("nanosecond value %d at timescale %d overflows ticks", n, timescale)
	}
	return q.Int64(), nil
}
