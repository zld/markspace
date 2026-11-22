// Package continuity tracks the 4-bit continuity counter of every PID
// and classifies anomalies: duplicate packets, counter gaps (with the
// number of lost packets) and transport-error packets. A gap is only
// recorded; it never causes later packets to be dropped.
package continuity

// EventKind classifies what Observe detected for one packet.
type EventKind int

const (
	EventOK EventKind = iota
	EventGap
	EventDuplicate
	EventTransportError
)

// Event describes the outcome of observing one packet.
type Event struct {
	Kind    EventKind
	Missing int // packets lost, only for EventGap
}

// Stats aggregates continuity anomalies of one PID.
type Stats struct {
	Gaps            int // number of counter discontinuities
	MissingPackets  int // packets lost across all gaps
	Duplicates      int // repeated counter values on payload packets
	TransportErrors int // packets with the transport error indicator
}

// Tracker keeps per-PID counter state. Memory is bounded by the
// 13-bit PID space.
type Tracker struct {
	last  map[uint16]uint8
	seen  map[uint16]bool
	stats map[uint16]*Stats
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{
		last:  map[uint16]uint8{},
		seen:  map[uint16]bool{},
		stats: map[uint16]*Stats{},
	}
}

// Observe classifies one packet and updates the PID's state. Packets
// flagged with a transport error are counted but never update counter
// state; adaptation-field-only packets do not advance the counter and
// are skipped, as required by the spec.
func (t *Tracker) Observe(pid uint16, cc uint8, hasPayload, transportError bool) Event {
	s := t.stats[pid]
	if s == nil {
		s = &Stats{}
		t.stats[pid] = s
	}
	if transportError {
		s.TransportErrors++
		return Event{Kind: EventTransportError}
	}
	if !hasPayload {
		return Event{Kind: EventOK}
	}
	if !t.seen[pid] {
		t.seen[pid], t.last[pid] = true, cc
		return Event{Kind: EventOK}
	}
	last := t.last[pid]
	switch expected := (last + 1) & 0x0F; {
	case cc == last:
		s.Duplicates++
		return Event{Kind: EventDuplicate}
	case cc != expected:
		missing := int(cc-last-1) & 0x0F
		s.Gaps++
		s.MissingPackets += missing
		t.last[pid] = cc
		return Event{Kind: EventGap, Missing: missing}
	default:
		t.last[pid] = cc
		return Event{Kind: EventOK}
	}
}

// Stats returns a snapshot of the per-PID aggregates.
func (t *Tracker) Stats() map[uint16]Stats {
	out := make(map[uint16]Stats, len(t.stats))
	for pid, s := range t.stats {
		out[pid] = *s
	}
	return out
}
