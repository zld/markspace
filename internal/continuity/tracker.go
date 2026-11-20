// Package continuity tracks per-PID continuity counters, duplicates and
// gap events in an MPEG-TS stream.
package continuity

import "tsmap/internal/ts"

// Event kinds recorded by the tracker.
const (
	EventGap           = "cc_gap"
	EventDuplicate     = "duplicate"
	EventDiscontinuity = "discontinuity"
)

// Event is a single continuity observation with its file offset.
type Event struct {
	PID      uint16
	Offset   int64
	Kind     string
	Expected byte
	Got      byte
	Lost     int
}

// PIDStats summarizes continuity observations for one PID.
type PIDStats struct {
	PID             uint16
	Packets         uint64 // packets seen (any adaptation_field_control)
	PayloadPackets  uint64 // packets carrying payload
	Duplicates      uint64 // same CC and identical content as previous packet
	GapEvents       uint64 // CC jumps (including same-CC/different-content)
	EstimatedLost   uint64 // packets estimated missing across all gaps
	Discontinuities uint64 // discontinuity_indicator assertions seen
}

type state struct {
	stats    PIDStats
	haveLast bool
	lastCC   byte
	lastRaw  [ts.PacketSize]byte
}

// Tracker keeps independent continuity state per PID.
type Tracker struct {
	per    map[uint16]*state
	events []Event
}

// New returns an empty Tracker.
func New() *Tracker { return &Tracker{per: make(map[uint16]*state)} }

// Events returns the recorded continuity events in observation order.
func (t *Tracker) Events() []Event { return t.events }

// Observe folds one packet into the per-PID continuity state.
func (t *Tracker) Observe(p *ts.Packet) {
	s := t.per[p.PID]
	if s == nil {
		s = &state{}
		s.stats.PID = p.PID
		t.per[p.PID] = s
	}
	s.stats.Packets++
	if p.HasPayload() {
		s.stats.PayloadPackets++
	}
	// Adaptation-field-only packets never advance the counter.
	if !p.HasPayload() {
		return
	}
	if p.DiscontinuityIndicator {
		s.stats.Discontinuities++
		t.events = append(t.events, Event{PID: p.PID, Offset: p.Offset, Kind: EventDiscontinuity})
		s.haveLast = true
		s.lastCC = p.ContinuityCounter
		s.lastRaw = p.Raw
		return
	}
	if !s.haveLast {
		s.haveLast = true
		s.lastCC = p.ContinuityCounter
		s.lastRaw = p.Raw
		return
	}
	expected := (s.lastCC + 1) & 0xf
	switch {
	case p.ContinuityCounter == expected:
		// normal advance
	case p.ContinuityCounter == s.lastCC && p.Raw == s.lastRaw:
		s.stats.Duplicates++
		t.events = append(t.events, Event{PID: p.PID, Offset: p.Offset, Kind: EventDuplicate,
			Expected: expected, Got: p.ContinuityCounter})
	default:
		s.stats.GapEvents++
		lost := (int(p.ContinuityCounter) - int(expected)) & 0xf
		s.stats.EstimatedLost += uint64(lost)
		t.events = append(t.events, Event{PID: p.PID, Offset: p.Offset, Kind: EventGap,
			Expected: expected, Got: p.ContinuityCounter, Lost: lost})
	}
	s.lastCC = p.ContinuityCounter
	s.lastRaw = p.Raw
}

// Stats returns per-PID statistics sorted by PID.
func (t *Tracker) Stats() []PIDStats {
	out := make([]PIDStats, 0, len(t.per))
	for _, s := range t.per {
		out = append(out, s.stats)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].PID < out[j-1].PID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// StatsFor returns the statistics for one PID.
func (t *Tracker) StatsFor(pid uint16) PIDStats {
	if s := t.per[pid]; s != nil {
		return s.stats
	}
	return PIDStats{PID: pid}
}
