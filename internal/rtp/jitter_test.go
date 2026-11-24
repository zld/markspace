package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts}}
}

// drain collects every event the buffer can currently produce.
func drain(b *Buffer) []Event {
	var out []Event
	for {
		ev := b.Next()
		if ev == nil {
			return out
		}
		out = append(out, *ev)
	}
}

func TestInOrderPassthrough(t *testing.T) {
	b := NewBuffer(Config{Window: 4})
	for i := uint16(0); i < 5; i++ {
		b.Push(pkt(i, uint32(i)*3600))
		evs := drain(b)
		if len(evs) != 1 || evs[0].Packet == nil || evs[0].Packet.SequenceNumber != i {
			t.Fatalf("seq %d: events = %+v", i, evs)
		}
	}
}

func TestGapWaitsForWindowThenLost(t *testing.T) {
	b := NewBuffer(Config{Window: 3})
	b.Push(pkt(0, 0))
	if evs := drain(b); len(evs) != 1 {
		t.Fatalf("seq0: %+v", evs)
	}
	// seq 1 goes missing. Packets 2 and 3 must not be emitted yet: the
	// window for seq 1 has not expired.
	b.Push(pkt(2, 7200))
	b.Push(pkt(3, 10800))
	if evs := drain(b); len(evs) != 0 {
		t.Fatalf("emitted before window expiry: %+v", evs)
	}
	// seq 4 pushes the gap past the window: seq 1 is declared lost and the
	// timeline advances.
	b.Push(pkt(4, 14400))
	evs := drain(b)
	var lost, emitted []uint16
	for _, ev := range evs {
		lost = append(lost, ev.Lost...)
		if ev.Packet != nil {
			emitted = append(emitted, ev.Packet.SequenceNumber)
		}
	}
	if len(lost) != 1 || lost[0] != 1 {
		t.Fatalf("lost = %v", lost)
	}
	if len(emitted) != 3 || emitted[0] != 2 || emitted[2] != 4 {
		t.Fatalf("emitted = %v", emitted)
	}
}

func TestLatePacketWithinWindow(t *testing.T) {
	b := NewBuffer(Config{Window: 3})
	b.Push(pkt(0, 0))
	drain(b)
	b.Push(pkt(2, 7200))
	if evs := drain(b); len(evs) != 0 {
		t.Fatalf("premature: %+v", evs)
	}
	b.Push(pkt(1, 3600)) // late but inside the window
	evs := drain(b)
	if len(evs) != 2 || evs[0].Packet.SequenceNumber != 1 || evs[1].Packet.SequenceNumber != 2 {
		t.Fatalf("reorder failed: %+v", evs)
	}
}

func TestFlushDeclaresRemainingGaps(t *testing.T) {
	b := NewBuffer(Config{Window: 8})
	b.Push(pkt(0, 0))
	b.Push(pkt(2, 7200)) // seq 1 never arrives, window never expires
	drain(b)
	evs := b.Flush()
	if len(evs) != 2 || len(evs[0].Lost) != 1 || evs[0].Lost[0] != 1 || evs[1].Packet.SequenceNumber != 2 {
		t.Fatalf("flush = %+v", evs)
	}
}

func TestTimestampJumpDetection(t *testing.T) {
	b := NewBuffer(Config{Window: 2, ExpectedDelta: 3600})
	if b.Push(pkt(0, 0)) {
		t.Fatal("first packet reported a jump")
	}
	if b.Push(pkt(1, 3600)) {
		t.Fatal("normal delta reported as jump")
	}
	if !b.Push(pkt(2, 3600+3600*10)) {
		t.Fatal("10x delta not reported as jump")
	}
}
