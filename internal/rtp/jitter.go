package rtp

import (
	"sort"
)

// JitterConfig bounds memory: at most Window sequence slots are held for
// reordering, and MaxPackets caps total buffered packets across all streams.
type JitterConfig struct {
	Window    uint64 // reorder window in extended sequence slots
	MaxPackets int    // hard cap on buffered packets
	TickNS    int64  // nanoseconds per RTP clock tick (from ClockRate)
}

type streamState struct {
	nextOut uint64
	gotBase bool
	pending map[uint64]Packet
	history map[uint64]Packet // recently emitted, bounded to Window slots for repair
	prev    *Packet
}

// JitterBuffer reorders packets per stream within a configured window,
// deduplicates repeats, marks out-of-window late arrivals, and invokes a
// bounded recoverer before emitting explicit gaps.
type JitterBuffer struct {
	cfg     JitterConfig
	streams map[StreamKey]*streamState
	recover Recoverer
	out     []Event
	totalIn int64
}

// NewJitterBuffer creates a buffer. A nil recoverer means every loss becomes
// an explicit gap event.
func NewJitterBuffer(cfg JitterConfig, r Recoverer) *JitterBuffer {
	if cfg.Window == 0 {
		cfg.Window = 32
	}
	if cfg.MaxPackets == 0 {
		cfg.MaxPackets = 1 << 20
	}
	return &JitterBuffer{cfg: cfg, streams: map[StreamKey]*streamState{}, recover: r}
}

func (b *JitterBuffer) state(k StreamKey) *streamState {
	s, ok := b.streams[k]
	if !ok {
		s = &streamState{pending: map[uint64]Packet{}, history: map[uint64]Packet{}}
		b.streams[k] = s
	}
	return s
}

func (b *JitterBuffer) emit(e Event) { b.out = append(b.out, e) }

// Push inserts one extended packet and drains everything that can be decided.
func (b *JitterBuffer) Push(p Packet) {
	b.totalIn++
	k := p.Key()
	s := b.state(k)
	if !s.gotBase {
		s.gotBase = true
		s.nextOut = p.ExtSeq
	}
	// Late: the sequence has already left the reorder window.
	if p.ExtSeq < s.nextOut {
		b.emit(Event{Kind: EventLate, Packet: p, ExtSeq: p.ExtSeq,
			Nanos: p.Nanos, SSRC: p.SSRC, PT: p.PT,
			Detail: "sequence outside reorder window"})
		return
	}
	// Duplicate: same extended sequence already buffered or emitted.
	if _, seen := s.pending[p.ExtSeq]; seen {
		b.emit(Event{Kind: EventDuplicate, Packet: p, ExtSeq: p.ExtSeq,
			Nanos: p.Nanos, SSRC: p.SSRC, PT: p.PT})
		return
	}
	if len(s.pending) >= b.cfg.MaxPackets {
		b.advance(k, s, p.ExtSeq) // force-drain oldest under memory pressure
	}
	s.pending[p.ExtSeq] = p
	// Drain once the oldest missing sequence has aged past the window. Using
	// the hole's age (rather than the highest sequence) keeps recovery groups
	// intact while their members are still inside the buffered window.
	if p.ExtSeq-s.nextOut >= b.cfg.Window {
		b.advance(k, s, p.ExtSeq)
	}
}

// present/payloadOf adapt both the pending window and the bounded emission
// history to the Recoverer interface.
func (b *JitterBuffer) present(k StreamKey, ext uint64) func(uint64) bool {
	return func(seq uint64) bool {
		s := b.streams[k]
		if _, ok := s.pending[seq]; ok {
			return true
		}
		_, ok := s.history[seq]
		return ok
	}
}

func (b *JitterBuffer) payloadOf(k StreamKey) func(uint64) []byte {
	return func(seq uint64) []byte {
		s := b.streams[k]
		if p, ok := s.pending[seq]; ok {
			return p.Payload
		}
		return s.history[seq].Payload
	}
}

func (b *JitterBuffer) remember(s *streamState, p Packet) {
	s.history[p.ExtSeq] = p
	// Retain everything inside one full reorder window behind the newest
	// emitted packet so repair groups spanning the window remain solvable.
	if p.ExtSeq <= 2*b.cfg.Window {
		return
	}
	cut := p.ExtSeq - 2*b.cfg.Window
	for ext := range s.history {
		if ext < cut {
			delete(s.history, ext)
		}
	}
}

// advance resolves every sequence up to and including trigger: present
// packets are emitted in order; holes are repaired when possible, otherwise
// emitted as explicit gaps.
func (b *JitterBuffer) advance(k StreamKey, s *streamState, trigger uint64) {
	for s.nextOut <= trigger {
		if p, ok := s.pending[s.nextOut]; ok {
			delete(s.pending, s.nextOut)
			b.emit(Event{Kind: EventPacket, Packet: p, ExtSeq: s.nextOut,
				Nanos: p.Nanos, SSRC: p.SSRC, PT: p.PT})
			b.remember(s, p)
			cp := p
			s.prev = &cp
			s.nextOut++
			continue
		}
		// Hole. Estimate its shared-base position from neighbours.
		var next *Packet
		if np, ok := s.pending[s.nextOut+1]; ok {
			next = &np
		}
		ns, _ := gapNanos(s.nextOut, s.prev, next, b.cfg.TickNS)
		b.repairHole(k, s, ns)
		s.nextOut++
	}
}

func (b *JitterBuffer) repairHole(k StreamKey, s *streamState, ns int64) {
	if b.recover != nil {
		if payload, rtpTime, ok2 := b.recover.Recover(s.nextOut, b.present(k, s.nextOut), b.payloadOf(k)); ok2 {
			p := Packet{SSRC: k.SSRC, PT: k.PT, ExtSeq: s.nextOut,
				Seq: uint16(s.nextOut), RTPTime: rtpTime, Nanos: ns, Payload: payload}
			b.emit(Event{Kind: EventRecovered, Packet: p, ExtSeq: s.nextOut,
				Nanos: ns, SSRC: k.SSRC, PT: k.PT, Detail: b.recover.Name()})
			b.remember(s, p)
			cp := p
			s.prev = &cp
			return
		}
	}
	// No sufficient repair information: record the hole, never fake media.
	b.emit(Event{Kind: EventGap, ExtSeq: s.nextOut, Nanos: ns,
		SSRC: k.SSRC, PT: k.PT, Detail: "unrecovered loss"})
}

// Flush drains all remaining streams; unresolved sequences become gaps.
func (b *JitterBuffer) Flush() []Event {
	keys := make([]StreamKey, 0, len(b.streams))
	for k := range b.streams {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].SSRC != keys[j].SSRC {
			return keys[i].SSRC < keys[j].SSRC
		}
		return keys[i].PT < keys[j].PT
	})
	for _, k := range keys {
		s := b.streams[k]
		if len(s.pending) == 0 {
			continue
		}
		high := uint64(0)
		for ext := range s.pending {
			if ext > high {
				high = ext
			}
		}
		b.advance(k, s, high)
	}
	out := b.out
	b.out = nil
	return out
}

// Stats summarizes buffer behaviour for reports.
type Stats struct {
	Input      int64
	Packets    int64
	Gaps       int64
	Recovered  int64
	Late       int64
	Duplicates int64
}

// CountStats tallies events (typically the slice returned by Flush).
func CountStats(events []Event) Stats {
	var st Stats
	for _, e := range events {
		switch e.Kind {
		case EventPacket:
			st.Packets++
		case EventGap:
			st.Gaps++
		case EventRecovered:
			st.Recovered++
		case EventLate:
			st.Late++
		case EventDuplicate:
			st.Duplicates++
		}
	}
	return st
}
