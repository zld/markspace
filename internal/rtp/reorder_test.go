package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func mkpkt(seq uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{SequenceNumber: seq, SSRC: 7}, Payload: []byte{0x80}}
}

func TestReorderInOrderAndGapRecovery(t *testing.T) {
	rb, err := NewReorderBuffer(4)
	if err != nil {
		t.Fatal(err)
	}
	// 1,2,4,3: packet 4 buffers a hole, 3 fills it before window expiry.
	for _, s := range []uint16{1, 2, 4, 3} {
		if st := rb.Push(mkpkt(s)); st != StatusBuffered {
			t.Fatalf("seq %d status %s", s, st)
		}
	}
	out := rb.Drain()
	got := make([]uint16, len(out))
	for i, p := range out {
		got[i] = p.SequenceNumber
	}
	want := []uint16{1, 2, 3, 4}
	if len(got) != 4 || got[0] != want[0] || got[3] != want[3] {
		t.Fatalf("drained %v want %v", got, want)
	}
	c := rb.Counters()
	if c.Recovered != 1 || c.Lost != 0 || c.InOrder != 3 {
		t.Fatalf("counters=%+v", c)
	}
}

func TestReorderLossThenLateIsImmutable(t *testing.T) {
	rb, _ := NewReorderBuffer(2)
	// Push 1, then 3..5; the hole at 2 is committed when 4 crosses the window.
	rb.Push(mkpkt(1))
	for _, s := range []uint16{3, 4, 5} {
		rb.Push(mkpkt(s))
	}
	c := rb.Counters()
	if c.Lost != 1 { // only seq 2 is missing
		t.Fatalf("expected one committed loss, got %+v", c)
	}
	if st := rb.Push(mkpkt(2)); st != StatusLate {
		t.Fatalf("late packet accepted as %s", st)
	}
	c = rb.Counters()
	if c.Late != 1 {
		t.Fatalf("late packet not counted: %+v", c)
	}
	if c.Lost != 1 {
		t.Fatalf("committed loss changed after late arrival: %+v", c)
	}
}

func TestReorderDuplicateAndOutOfWindow(t *testing.T) {
	rb, _ := NewReorderBuffer(3)
	rb.Push(mkpkt(10))
	if st := rb.Push(mkpkt(10)); st != StatusDuplicate {
		t.Fatalf("dup classified as %s", st)
	}
	if st := rb.Push(mkpkt(100)); st != StatusOutOfWindow {
		t.Fatalf("far packet classified as %s", st)
	}
	c := rb.Counters()
	if c.Duplicates != 1 || c.OutOfWindow != 1 {
		t.Fatalf("counters=%+v", c)
	}
}

func TestReorderFlushCommitsTailHole(t *testing.T) {
	rb, _ := NewReorderBuffer(4)
	rb.Push(mkpkt(1))
	rb.Push(mkpkt(2))
	rb.Push(mkpkt(4)) // hole at 3 stays inside window until flush
	out := rb.Flush()
	if len(out) != 3 {
		t.Fatalf("flush emitted %d packets", len(out))
	}
	if c := rb.Counters(); c.Lost != 1 {
		t.Fatalf("tail hole not committed: %+v", c)
	}
}

func TestReorderWindowBounds(t *testing.T) {
	if _, err := NewReorderBuffer(1); err == nil {
		t.Fatal("window below minimum accepted")
	}
	if _, err := NewReorderBuffer(0); err == nil {
		t.Fatal("zero window accepted")
	}
}
