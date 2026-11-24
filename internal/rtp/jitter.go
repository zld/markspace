// Package rtp provides a bounded jitter buffer with packet-loss recovery.
// Missing sequence numbers are awaited up to a configured window; when the
// window expires the gap is declared lost and the timeline advances. No
// concealment samples are ever fabricated.
package rtp

import (
	"github.com/pion/rtp"
)

// Config tunes one Buffer.
type Config struct {
	// Window is how many sequence numbers ahead we wait for a missing
	// packet before declaring it lost.
	Window uint16
	// ExpectedDelta is the expected RTP timestamp step between packets;
	// 0 disables timestamp-jump detection.
	ExpectedDelta uint32
}

// Event is one buffer output: either an emitted packet, one or more lost
// sequence numbers, or both (loss reported together with the next packet).
type Event struct {
	Packet        *rtp.Packet
	Lost          []uint16
	TimestampJump bool
}

// Buffer is a single-track reordering/loss-recovery buffer. Memory is
// bounded by the window: at most Window+1 packets are held.
type Buffer struct {
	cfg      Config
	started  bool
	expected uint16
	maxSeq   uint16
	pending  map[uint16]*rtp.Packet
	lastTS   uint32
	hasTS    bool
}

func NewBuffer(cfg Config) *Buffer {
	if cfg.Window == 0 {
		cfg.Window = 1
	}
	return &Buffer{cfg: cfg, pending: map[uint16]*rtp.Packet{}}
}

// Push inserts one packet. It reports whether the RTP timestamp jumped
// forward by more than 4x the expected delta (discontinuity).
func (b *Buffer) Push(p *rtp.Packet) (jump bool) {
	seq := p.SequenceNumber
	if !b.started {
		b.started = true
		b.expected = seq
		b.maxSeq = seq
	} else if seqAfter(seq, b.maxSeq) {
		b.maxSeq = seq
	}
	if b.cfg.ExpectedDelta > 0 {
		if b.hasTS {
			delta := p.Timestamp - b.lastTS
			if delta > 4*b.cfg.ExpectedDelta {
				jump = true
			}
		}
		b.lastTS = p.Timestamp
		b.hasTS = true
	}
	if len(b.pending) <= int(b.cfg.Window) {
		cp := *p
		b.pending[seq] = &cp
	}
	return jump
}

// Next returns the next event, or nil when more input (or a flush) is
// needed to advance.
func (b *Buffer) Next() *Event {
	if !b.started {
		return nil
	}
	if p, ok := b.pending[b.expected]; ok {
		delete(b.pending, b.expected)
		b.expected++
		return &Event{Packet: p}
	}
	// Gap at head of line: wait until the window expires, then declare the
	// missing sequence lost and advance the timeline.
	if seqDiff(b.maxSeq, b.expected) >= b.cfg.Window {
		lost := b.expected
		b.expected++
		return &Event{Lost: []uint16{lost}}
	}
	return nil
}

// Flush drains the buffer at end of stream: every remaining gap is declared
// lost, buffered packets are emitted in sequence order.
func (b *Buffer) Flush() []Event {
	var out []Event
	if !b.started {
		return out
	}
	for b.expected != b.maxSeq+1 {
		if p, ok := b.pending[b.expected]; ok {
			delete(b.pending, b.expected)
			out = append(out, Event{Packet: p})
		} else {
			out = append(out, Event{Lost: []uint16{b.expected}})
		}
		b.expected++
	}
	return out
}

// seqAfter reports whether a is ahead of b in uint16 sequence space.
func seqAfter(a, b uint16) bool { return int16(a-b) > 0 }

// seqDiff returns a-b in sequence space (valid when a is not behind b).
func seqDiff(a, b uint16) uint16 { return a - b }
