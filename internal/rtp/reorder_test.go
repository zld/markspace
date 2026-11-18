package rtp

import (
	"testing"
	"time"
)

func mkPkt(seq uint16, ts uint32, marker bool) *Packet {
	return &Packet{
		Header: Header{SequenceNumber: seq, Timestamp: ts, Marker: marker, SSRC: 1},
		Payload: []byte{byte(seq)},
	}
}

func seqs(out []*Packet) []uint16 {
	got := make([]uint16, len(out))
	for i, p := range out {
		got[i] = p.Header.SequenceNumber
	}
	return got
}

func TestReorderInOrderAndWraparound(t *testing.T) {
	r := NewReorderer(8, 25*time.Millisecond)
	now := time.Unix(0, 0)
	// Deliver across the uint16 boundary slightly out of order so buffering
	// is exercised: 0xfffe first, then 0x0000, finally 0xffff.
	if out := r.Push(mkPkt(0xfffe, 1, false), now); len(out) != 1 {
		t.Fatalf("first packet should emit immediately: %v", seqs(out))
	}
	r.Push(mkPkt(0x0000, 1, false), now) // buffered beyond 0xffff
	out := r.Push(mkPkt(0xffff, 1, true), now)
	if len(out) != 2 || seqs(out)[0] != 0xffff || seqs(out)[1] != 0 {
		t.Fatalf("wraparound order wrong: %v", seqs(out))
	}
	if r.Stats().Received != 3 || r.Stats().Duplicates != 0 {
		t.Fatalf("unexpected stats: %+v", r.Stats())
	}
}

func TestReorderReordering(t *testing.T) {
	r := NewReorderer(8, 25*time.Millisecond)
	now := time.Unix(0, 0)
	if out := r.Push(mkPkt(0, 1, false), now); len(out) != 1 {
		t.Fatalf("first packet should emit immediately: %v", seqs(out))
	}
	if out := r.Push(mkPkt(2, 1, false), now); len(out) != 0 {
		t.Fatalf("gap packet must stay buffered: %v", seqs(out))
	}
	out := r.Push(mkPkt(1, 1, false), now)
	if seqs(out)[0] != 1 || seqs(out)[1] != 2 {
		t.Fatalf("reorder failed: %v", seqs(out))
	}
}

func TestReorderWindowOverflowRecordsGap(t *testing.T) {
	r := NewReorderer(4, 25*time.Millisecond)
	now := time.Unix(0, 0)
	r.Push(mkPkt(0, 1, false), now)
	// seq 1..6 missing; seq 7 is exactly window distance from expected 1.
	out := r.Push(mkPkt(7, 1, false), now)
	if len(out) != 1 || seqs(out)[0] != 7 {
		t.Fatalf("overflow should flush only the far packet, got %v", seqs(out))
	}
	st := r.Stats()
	if st.MissingPackets != 6 || st.GapEvents != 1 {
		t.Fatalf("gap accounting wrong: %+v", st)
	}
	if len(r.entries) != 0 {
		t.Fatalf("buffer must stay bounded, entries=%d", len(r.entries))
	}
}

func TestReorderDuplicateAndLate(t *testing.T) {
	r := NewReorderer(8, 25*time.Millisecond)
	now := time.Unix(0, 0)
	r.Push(mkPkt(10, 1, false), now)
	r.Push(mkPkt(11, 1, false), now)
	if out := r.Push(mkPkt(10, 1, false), now); len(out) != 0 {
		t.Fatalf("duplicate emitted: %v", seqs(out))
	}
	// A late packet older than the frontier (after a gap flush) is rejected
	// immediately and can never enter an already emitted frame again.
	r.Push(mkPkt(19, 1, false), now) // overflow: flushes 12..18 gap, emits 19
	if out := r.Push(mkPkt(12, 1, false), now); len(out) != 0 {
		t.Fatalf("late packet re-entered output: %v", seqs(out))
	}
	if out := r.Tick(now.Add(25 * time.Millisecond)); len(out) != 0 {
		t.Fatalf("late packet leaked through timeout flush: %v", seqs(out))
	}
	st := r.Stats()
	if st.Duplicates != 2 {
		t.Fatalf("duplicate counter wrong: %+v", st)
	}
	if st.MissingPackets != 6 { // 12..18 flushed by the window overflow
		t.Fatalf("missing counter wrong: %+v", st)
	}
}

func TestReorderTimeoutFlush(t *testing.T) {
	r := NewReorderer(8, 25*time.Millisecond)
	t0 := time.Unix(0, 0)
	r.Push(mkPkt(0, 1, false), t0)
	r.Push(mkPkt(2, 1, false), t0) // seq 1 missing
	if out := r.Tick(t0.Add(24 * time.Millisecond)); out != nil {
		t.Fatalf("flush before timeout: %v", seqs(out))
	}
	out := r.Tick(t0.Add(25 * time.Millisecond))
	if len(out) != 1 || seqs(out)[0] != 2 {
		t.Fatalf("timeout flush wrong: %v", seqs(out))
	}
	st := r.Stats()
	if st.TimeoutFlushes != 1 || st.MissingPackets != 1 || st.GapEvents != 1 {
		t.Fatalf("timeout stats wrong: %+v", st)
	}
}

func TestReorderReset(t *testing.T) {
	r := NewReorderer(8, 25*time.Millisecond)
	now := time.Unix(0, 0)
	r.Push(mkPkt(0, 1, false), now)
	r.Push(mkPkt(2, 1, false), now)
	r.Reset()
	if r.init || len(r.entries) != 0 || r.Stats() != (ReorderStats{}) {
		t.Fatalf("reset left residue: %+v entries=%d", r.Stats(), len(r.entries))
	}
	// After reset the next sequence becomes the new anchor, no stale gap.
	out := r.Push(mkPkt(100, 1, false), now)
	if len(out) != 1 || seqs(out)[0] != 100 {
		t.Fatalf("post-reset anchor wrong: %v", seqs(out))
	}
}
