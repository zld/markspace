package rtp

import "testing"

func pkt(seq uint16) *Packet {
	return &Packet{SequenceNumber: seq, SSRC: 1, Timestamp: uint32(seq) * 3600}
}

func seqs(pkts []*Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.SequenceNumber
	}
	return out
}

func TestReorderInOrder(t *testing.T) {
	r, err := NewReorderer(8)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint16(0); i < 10; i++ {
		out := r.Push(pkt(i))
		if len(out) != 1 || out[0].SequenceNumber != i {
			t.Fatalf("seq %d: emitted %v, want immediate single output", i, seqs(out))
		}
	}
	s := r.Stats()
	if s.Gaps != 0 || s.Duplicates != 0 || s.Emitted != 10 || s.Received != 10 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestReorderOutOfOrderWithinWindow(t *testing.T) {
	r, _ := NewReorderer(8)
	// 0,1 then hole at 2, arrive 4,3,2.
	r.Push(pkt(0))
	r.Push(pkt(1))
	if out := r.Push(pkt(4)); len(out) != 0 {
		t.Fatalf("4 should be held, got %v", seqs(out))
	}
	if out := r.Push(pkt(3)); len(out) != 0 {
		t.Fatalf("3 should be held, got %v", seqs(out))
	}
	out := r.Push(pkt(2))
	if got := seqs(out); len(got) != 3 || got[0] != 2 || got[2] != 4 {
		t.Fatalf("held packets released out of order: %v", got)
	}
	if r.Stats().Gaps != 0 || r.Pending() != 0 {
		t.Fatalf("unexpected stats: %+v pending=%d", r.Stats(), r.Pending())
	}
}

func TestReorderDuplicates(t *testing.T) {
	r, _ := NewReorderer(8)
	r.Push(pkt(0))
	// Re-send an already emitted packet: late, not re-emitted.
	if out := r.Push(pkt(0)); out != nil {
		t.Fatalf("re-send of emitted packet must produce no output, got %v", seqs(out))
	}
	if r.Stats().Late != 1 {
		t.Fatalf("late = %d, want 1", r.Stats().Late)
	}
	r.Push(pkt(2)) // held behind missing 1
	// Exact re-delivery of a pending packet: true duplicate.
	if out := r.Push(pkt(2)); out != nil {
		t.Fatalf("duplicate of pending packet must produce no output, got %v", seqs(out))
	}
	if r.Stats().Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", r.Stats().Duplicates)
	}
}

func TestReorderWraparound(t *testing.T) {
	r, _ := NewReorderer(8)
	for _, seq := range []uint16{65534, 65535} {
		if out := r.Push(pkt(seq)); len(out) != 1 {
			t.Fatalf("seq %d should pass through", seq)
		}
	}
	// Hole at 0; 2 then 1 then 0 arrive across the 16-bit boundary.
	if out := r.Push(pkt(2)); len(out) != 0 {
		t.Fatalf("2 should be held across wraparound, got %v", seqs(out))
	}
	r.Push(pkt(1))
	out := r.Push(pkt(0))
	if got := seqs(out); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("wraparound reorder failed: %v", got)
	}
	if r.Stats().Gaps != 0 {
		t.Fatalf("no gap expected, got %d", r.Stats().Gaps)
	}
}

func TestReorderWindowExhaustion(t *testing.T) {
	r, _ := NewReorderer(4)
	r.Push(pkt(0))
	r.Push(pkt(1))
	// seq 2 is missing; fill the window with 3..6.
	r.Push(pkt(3))
	r.Push(pkt(4))
	r.Push(pkt(5))
	out := r.Push(pkt(6)) // window full -> advance over the hole
	if got := seqs(out); len(got) != 4 || got[0] != 3 || got[3] != 6 {
		t.Fatalf("window flush output = %v, want 3,4,5,6", got)
	}
	s := r.Stats()
	if s.Gaps != 1 || s.Flushed != 1 {
		t.Fatalf("gap accounting wrong: %+v", s)
	}
	// The skipped slot is now in the past: late arrival is not re-emitted.
	if out := r.Push(pkt(2)); out != nil {
		t.Fatalf("late packet after advance must not be emitted, got %v", seqs(out))
	}
	if r.Stats().Late != 1 {
		t.Fatalf("late = %d, want 1", r.Stats().Late)
	}
}

func TestReorderJumpBeyondWindow(t *testing.T) {
	r, _ := NewReorderer(4)
	r.Push(pkt(0))
	r.Push(pkt(1)) // expected = 2
	out := r.Push(pkt(10))
	if len(out) != 1 || out[0].SequenceNumber != 10 {
		t.Fatalf("jump packet should be emitted, got %v", seqs(out))
	}
	s := r.Stats()
	if s.Gaps != 8 || s.Flushed != 1 {
		t.Fatalf("jump gap accounting wrong: %+v", s)
	}
}

func TestReorderFlushRecordsTrailingGap(t *testing.T) {
	r, _ := NewReorderer(8)
	r.Push(pkt(0)) // expected = 1
	r.Push(pkt(2))
	r.Push(pkt(3))
	out := r.Flush()
	if got := seqs(out); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("flush output = %v, want 2,3", got)
	}
	if r.Stats().Gaps != 1 || r.Pending() != 0 {
		t.Fatalf("flush gap accounting wrong: %+v", r.Stats())
	}
}

func TestNewReordererRejectsBadWindow(t *testing.T) {
	for _, w := range []int{0, -1, maxWindow + 1} {
		if _, err := NewReorderer(w); err != ErrInvalidWindow {
			t.Fatalf("window %d: got %v, want ErrInvalidWindow", w, err)
		}
	}
}
