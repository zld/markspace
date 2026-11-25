// Package clock turns raw transport timestamps into one monotonic media
// timeline. PCR values are 33+9 bit quantities that wrap every ~26.5 hours;
// the timeline unwraps them into a single 27 MHz tick space shared by every
// representation, and remembers corruption spans so they can be propagated
// to affected segments.
package clock

import "mediad/internal/model"

// ClockEvent marks a discontinuity observed on the media timeline.
type ClockEvent struct {
	Kind   string // "wrap" | "jump" | "rtp"
	Offset int64
	Delta  int64 // signed jump in 27 MHz ticks
}

// CorruptionSpan is the media-time range during which one PID is known to be
// unreliable. Spans run from the first bad sample to the end of the stream
// because a lost packet cannot be reconstructed.
type CorruptionSpan struct {
	StartTicks int64
	Offset     int64
	PID        uint16
}

// pcrModulus is the full PCR period in 27 MHz ticks: 2^33 * 300.
const pcrModulus = model.PcrBaseModulus * 300

// Timeline maintains unwrapped PCR state for one PCR PID.
type Timeline struct {
	started      bool
	lastRaw      int64
	lastUnwrap   int64
	base         int64 // unwrapped value that maps to media time zero
	currentMedia int64
	events       []ClockEvent
	spans        []CorruptionSpan
}

// NewTimeline creates an empty timeline.
func NewTimeline() *Timeline { return &Timeline{} }

// FeedPCR incorporates one raw PCR value sampled at input offset. It returns
// the corresponding monotonic media time in 27 MHz ticks and true when the
// sample completed a PCR wraparound. A backward modular distance larger than
// half the period is treated as a wrap; smaller backward motion is reported
// as a jump but never moves media time backwards.
func (t *Timeline) FeedPCR(raw, offset int64) (media int64, wrapped bool, err error) {
	if raw < 0 || raw >= pcrModulus {
		return 0, false, &model.CorruptError{
			Kind: "pcr", Offset: offset, Actual: uint16(raw),
			Detail: "PCR value outside 33+9 bit range",
		}
	}
	if !t.started {
		t.started = true
		t.lastRaw = raw
		t.lastUnwrap = raw
		t.base = raw
		return 0, false, nil
	}
	delta := raw - t.lastRaw
	if delta < 0 {
		modDelta := delta + pcrModulus
		if modDelta <= pcrModulus/2 {
			// Forward distance across the wrap boundary is short: real wrap.
			delta = modDelta
			wrapped = true
		}
	}
	switch {
	case wrapped:
		t.events = append(t.events, ClockEvent{Kind: "wrap", Offset: offset, Delta: delta})
	case delta < 0:
		t.events = append(t.events, ClockEvent{Kind: "jump", Offset: offset, Delta: delta})
		delta = 0 // keep media time monotonic; do not fabricate samples
	case delta > model.ClockHz/10:
		// Forward gap larger than 100 ms is a suspicious discontinuity.
		t.events = append(t.events, ClockEvent{Kind: "jump", Offset: offset, Delta: delta})
	}
	t.lastRaw = raw
	t.lastUnwrap += delta
	t.currentMedia = t.lastUnwrap - t.base
	return t.currentMedia, wrapped, nil
}

// MediaTicks returns the most recently produced media time.
func (t *Timeline) MediaTicks() int64 { return t.currentMedia }

// MarkCorruption records that pid became unreliable at offset, at the current
// media time. Later segment lookups use the span to flag affected output.
func (t *Timeline) MarkCorruption(offset int64, pid uint16) {
	t.spans = append(t.spans, CorruptionSpan{
		StartTicks: t.currentMedia, Offset: offset, PID: pid,
	})
}

// AffectedPIDs returns every PID whose corruption span starts at or before
// the given media time. The result is sorted and deduplicated.
func (t *Timeline) AffectedPIDs(mediaTicks int64) []uint16 {
	seen := map[uint16]struct{}{}
	var out []uint16
	for _, s := range t.spans {
		if s.StartTicks <= mediaTicks {
			if _, ok := seen[s.PID]; !ok {
				seen[s.PID] = struct{}{}
				out = append(out, s.PID)
			}
		}
	}
	return out
}

// Events returns all clock discontinuities recorded so far.
func (t *Timeline) Events() []ClockEvent { return t.events }

// Spans returns all corruption spans.
func (t *Timeline) Spans() []CorruptionSpan { return t.spans }
