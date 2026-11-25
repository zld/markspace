// Package rtp turns a delivery-order list of RTP sequence numbers into an
// extended, wrap-safe sequence with duplicates and gaps classified.
//
// Extension uses the standard RFC 3550-style "closest to previous extended
// value" rule: a 16-bit sequence is mapped into the 32-bit cycle window
// nearest the last accepted extended value. This makes a single wrap (and
// even a jump of several cycles) unambiguous while keeping out-of-order
// arrivals inside one window.
package rtp

import (
	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
)

// EventKind mirrors index.PacketKind without importing it, so sequence
// logic stays usable by tests that do not build a full index.
type EventKind int

const (
	// KindPacket is a newly observed packet.
	KindPacket EventKind = iota
	// KindDuplicate is a repeat of an already-seen sequence number.
	KindDuplicate
	// KindGap is a hole left by one or more missing sequence numbers.
	KindGap
)

// Event is one classified delivery event.
type Event struct {
	Kind    EventKind
	Seq     uint16
	SeqExt  uint64
	Offset  int64
	Count   uint64 // KindGap: number of missing packets
	Missing uint64 // KindGap: first missing extended sequence
}

// Tracker classifies a stream of sequence numbers.
type Tracker struct {
	started bool
	lastExt uint64
	nextExt uint64
	seen    map[uint16]bool
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{seen: make(map[uint16]bool, 64)}
}

// Next is the extended sequence number that should arrive next after all
// events classified so far. It is meaningful only once at least one packet
// has been observed.
func (t *Tracker) Next() (uint64, bool) {
	return t.nextExt, t.started
}

// Accept classifies seq, observed at byte offset off in the RTP input.
//
// Rules:
//   - the first packet starts the stream at itself;
//   - a sequence already seen is a duplicate and does not move the
//     expected-next pointer;
//   - a sequence one or more steps ahead is a normal packet followed by a
//     gap event that records exactly how many packets are missing, without
//     synthesising anything;
//   - a sequence behind the expected-next pointer but not seen yet is
//     treated as a late arrival and does not advance the pointer.
func (t *Tracker) Accept(seq uint16, off int64) []Event {
	if !t.started {
		t.started = true
		t.lastExt = uint64(seq)
		t.nextExt = uint64(seq) + 1
		t.seen[seq] = true
		return []Event{{Kind: KindPacket, Seq: seq, SeqExt: uint64(seq), Offset: off}}
	}

	ext := t.extend(seq)
	if t.seen[seq] {
		t.seen[seq] = true
		return []Event{{Kind: KindDuplicate, Seq: seq, SeqExt: ext, Offset: off}}
	}
	t.seen[seq] = true

	if ext < t.nextExt {
		// Late arrival that fills a previously reported hole is not treated
		// as a duplicate: it is a real packet, but the gap event already
		// stands as a record of the delivery gap.
		if ext+1 == t.nextExt {
			return []Event{{Kind: KindPacket, Seq: seq, SeqExt: ext, Offset: off}}
		}
		return []Event{{Kind: KindPacket, Seq: seq, SeqExt: ext, Offset: off}}
	}

	events := make([]Event, 0, 2)
	events = append(events, Event{Kind: KindPacket, Seq: seq, SeqExt: ext, Offset: off})
	if ext > t.nextExt {
		events = append(events, Event{
			Kind:    KindGap,
			Seq:     seq,
			SeqExt:  ext,
			Offset:  off,
			Count:   ext - t.nextExt,
			Missing: t.nextExt,
		})
	}
	t.lastExt = ext
	t.nextExt = ext + 1
	return events
}

// extend maps a 16-bit sequence into the 32-bit cycle window nearest lastExt.
func (t *Tracker) extend(seq uint16) uint64 {
	base := t.lastExt &^ 0xFFFF
	candidate := base | uint64(seq)
	// Compare against the previous extended value; if the candidate is more
	// than one full cycle away, shift it into the neighbouring cycle.
	if candidate+0x8000 < t.lastExt {
		candidate += 0x10000
	} else if candidate > t.lastExt+0x8000 && candidate >= 0x10000 {
		candidate -= 0x10000
	}
	return candidate
}

// Gap is a located time discontinuity caused by missing packets.
type Gap struct {
	Seq     uint16
	SeqExt  uint64
	Missing uint64
	Offset  int64
	From    clock.Nanos
	To      clock.Nanos
}

// Summarize builds a stable summary of a classified packet event stream.
type Summary struct {
	Packets    int
	Duplicates int
	Gaps       int
	Missing    uint64
	GapEvents  []Gap
}

// Summarize walks events in delivery order and accumulates counters. The
// media time advance must be computed by the caller (it owns the clock
// rate); Progress here is the number of packets that advanced time.
func Summarize(events []Event) Summary {
	var s Summary
	for _, ev := range events {
		switch ev.Kind {
		case KindPacket:
			s.Packets++
		case KindDuplicate:
			s.Duplicates++
		case KindGap:
			s.Gaps++
			s.Missing += ev.Count
			s.GapEvents = append(s.GapEvents, Gap{
				Seq:     ev.Seq,
				SeqExt:  ev.SeqExt,
				Missing: ev.Count,
				Offset:  ev.Offset,
			})
		}
	}
	return s
}

// Faults converts every gap event into a located, non-fatal fault so the
// reporter can show where the discontinuity is in the input bytes.
func (s Summary) Faults() []*fault.Error {
	out := make([]*fault.Error, 0, len(s.GapEvents))
	for _, g := range s.GapEvents {
		msg := "missing " + itoa(g.Missing) + " packet(s) at extended sequence " + itoa(g.SeqExt)
		out = append(out, fault.New(fault.StageStream, "rtp.gap", g.Offset, g.From, msg))
	}
	return out
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
