package rtprecover

import (
	"testing"

	"github.com/pion/rtp"

	merr "mediatool/internal/errors"
)

func mkPacket(ssrc uint32, seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SSRC: ssrc, SequenceNumber: seq, Timestamp: uint32(seq) * 160},
		Payload: []byte{byte(seq), byte(seq >> 8)},
	}
}

func pushSeq(t *testing.T, w *Window, seqs ...uint16) {
	t.Helper()
	for _, s := range seqs {
		if err := w.Push(mkPacket(w.ssrc, s)); err != nil {
			t.Fatalf("push seq %d: %v", s, err)
		}
	}
}

func TestInOrderDrain(t *testing.T) {
	w := NewWindow(0xABCD, 8)
	pushSeq(t, w, 0, 1, 2, 3)
	out := w.Drain()
	if len(out) != 4 || out[2].Seq != 2 {
		t.Fatalf("drain = %v", out)
	}
	if st := w.Stats(); st.Lost != 0 || st.Reordered != 0 || st.Duplicates != 0 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestDuplicatesCountedAndDropped(t *testing.T) {
	w := NewWindow(1, 8)
	pushSeq(t, w, 5, 6, 5, 6, 5)
	if st := w.Stats(); st.Duplicates != 3 || st.Received != 5 {
		t.Fatalf("dup stats: %+v", st)
	}
	out := w.Drain()
	if len(out) != 2 {
		t.Fatalf("duplicates leaked into stream: %d packets", len(out))
	}
}

func TestReorderRecoveredInsideWindow(t *testing.T) {
	w := NewWindow(1, 16)
	// 0,1 then a swapped pair: 3 arrives before 2. No Drain in between, so
	// the missing seq 2 stays an open hole inside the high-water mark.
	pushSeq(t, w, 0, 1, 3)
	pushSeq(t, w, 2)
	out := w.Drain()
	if len(out) != 4 {
		t.Fatalf("after fill, want 4 packets, got %d", len(out))
	}
	for i, p := range out {
		if p.Seq != uint16(i) {
			t.Fatalf("order broken at %d: %d", i, p.Seq)
		}
	}
	st := w.Stats()
	if st.Recoverable != 1 || st.Reordered != 1 || st.Lost != 0 {
		t.Fatalf("recovery stats wrong: %+v", st)
	}
}

func TestBurstLossOnOverflow(t *testing.T) {
	w := NewWindow(1, 4)
	// 0 then jump to 5: positions 1..4 missing. Window holds {0,5..} and
	// overflows, forcing the gap to be declared lost rather than fabricated.
	pushSeq(t, w, 0)
	if err := w.Push(mkPacket(1, 5)); err != nil {
		t.Fatal(err)
	}
	for s := uint16(6); s <= 9; s++ {
		if err := w.Push(mkPacket(1, s)); err != nil {
			t.Fatal(err)
		}
	}
	st := w.Stats()
	if st.Lost < 4 {
		t.Fatalf("expected >=4 lost, got %d", st.Lost)
	}
	gaps := w.Unrecoverable()
	if len(gaps) == 0 {
		t.Fatal("unrecoverable gaps not recorded")
	}
	out := w.Finalize()
	// No fabricated seqs: every delivered packet must be a real pushed one.
	seen := map[uint16]bool{}
	for _, p := range out {
		if seen[p.Seq] {
			t.Fatalf("fabricated/duplicate seq %d", p.Seq)
		}
		seen[p.Seq] = true
	}
}

func TestLatePacketRejected(t *testing.T) {
	w := NewWindow(1, 4)
	pushSeq(t, w, 0, 1, 2, 3, 4, 5, 6) // forces evictions, base advances
	w.Drain()
	// A very old sequence can never re-enter.
	err := w.Push(mkPacket(1, 0))
	if err == nil || merr.AsCode(err) != merr.CodeLateOutsideWindow {
		t.Fatalf("late packet not rejected: %v", err)
	}
	if w.Stats().LateRejected != 1 {
		t.Fatalf("late counter = %d", w.Stats().LateRejected)
	}
}

func TestSSRCMismatch(t *testing.T) {
	w := NewWindow(1, 4)
	err := w.Push(mkPacket(2, 0))
	if err == nil || merr.AsCode(err) != merr.CodeSSRCMismatch {
		t.Fatalf("ssrc check failed: %v", err)
	}
}

func TestFinalizeMarksTrailingGap(t *testing.T) {
	w := NewWindow(1, 16)
	pushSeq(t, w, 10, 11, 14) // 12,13 missing, no overflow yet
	out := w.Finalize()
	if w.Stats().Lost != 2 {
		t.Fatalf("trailing gap lost = %d, want 2", w.Stats().Lost)
	}
	for _, p := range out {
		if p.Seq == 12 || p.Seq == 13 {
			t.Fatal("missing seq fabricated at finalize")
		}
	}
}
