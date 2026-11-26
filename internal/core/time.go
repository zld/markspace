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

// EpochTracker assigns canonical nanoseconds to raw timestamps while keeping
// the canonical timeline monotonic across discontinuities. It is used
// identically by the scanner and by the streaming runner so both classify
// jumps the same way.
type EpochTracker struct {
	epochs    []Epoch
	current   Epoch
	have      bool
	lastCanon Nanos
	// rawUnit is informational only: "ticks" or "rtp".
	rawUnit string
}

// NewEpochTracker starts a tracker with a human-readable raw unit name.
func NewEpochTracker(rawUnit string) *EpochTracker {
	return &EpochTracker{rawUnit: rawUnit}
}

// Epochs returns the epochs opened so far, in order.
func (t *EpochTracker) Epochs() []Epoch { return t.epochs }

// Current returns the active epoch.
func (t *EpochTracker) Current() Epoch { return t.current }

// Open starts a new epoch. rawBase is the raw value of the first timestamp in
// the epoch and canonicalBase its assigned canonical position; delta is chosen
// so that the mapping is exact and monotonic.
func (t *EpochTracker) Open(reason EpochReason, source string, rawBase int64, rawAsNanos Nanos, canonicalBase Nanos) Epoch {
	prevDelta := Nanos(0)
	if t.have {
		prevDelta = t.current.Delta
	}
	delta := canonicalBase - rawAsNanos
	ep := Epoch{
		Number:         len(t.epochs),
		Reason:         reason,
		Source:         source,
		RawBase:        rawBase,
		CanonicalBase:  canonicalBase,
		Delta:          delta,
		BoundaryOffset: delta - prevDelta,
	}
	t.epochs = append(t.epochs, ep)
	t.current = ep
	t.have = true
	t.lastCanon = canonicalBase
	return ep
}

// Map converts a raw timestamp in the current epoch to a canonical position.
// It returns the position and whether that position advanced the canonical
// timeline; a value that would go backwards is clamped to lastCanon so the
// output stays monotonic, and the caller is told so it can record a fault.
func (t *EpochTracker) Map(rawAsNanos Nanos) (Nanos, bool) {
	pos := rawAsNanos + t.current.Delta
	if pos < t.lastCanon {
		return t.lastCanon, false
	}
	t.lastCanon = pos
	return pos, true
}

// Last returns the greatest canonical position emitted so far.
func (t *EpochTracker) Last() Nanos { return t.lastCanon }
