package rtprecover

import (
	"testing"

	"github.com/pion/rtp"

	"github.com/archive/mediakernel/internal/timeline"
)

func pkt(seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: uint32(seq) * 160},
		Payload: []byte{byte(seq), byte(seq >> 8)},
	}
}

func push(t *testing.T, w *Window, seq uint16) []OrderedPacket {
	t.Helper()
	out, err := w.Push(pkt(seq), int64(seq)*20_000)
	if err != nil {
		t.Fatalf("Push(%d): %v", seq, err)
	}
	return out
}

func seqs(out []OrderedPacket) []uint16 {
	got := make([]uint16, 0, len(out))
	for _, op := range out {
		got = append(got, op.Packet.SequenceNumber)
	}
	return got
}

func TestDuplicatesNeverEmitted(t *testing.T) {
	w, err := NewWindow(8)
	if err != nil {
		t.Fatal(err)
	}
	var total []OrderedPacket
	// Original 1,2,3 then retransmissions of every one, including after release.
	for _, s := range []uint16{1, 2, 3, 1, 2, 3, 4, 3, 4} {
		total = append(total, push(t, w, s)...)
	}
	total = append(total, w.Flush()...)
	got := seqs(total)
	want := []uint16{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v (duplicate output must be zero)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("emitted %v, want %v", got, want)
		}
	}
	st := w.Stats()
	if st.Duplicate != 5 {
		t.Fatalf("duplicate count = %d, want 5", st.Duplicate)
	}
	if st.Late != 0 {
		t.Fatalf("late count = %d, want 0", st.Late)
	}
}

func TestReorderingReleasedInSequence(t *testing.T) {
	w, _ := NewWindow(8)
	var total []OrderedPacket
	// 0 first anchors, then 3,2,1 arrive out of order; nothing emits until 1.
	total = append(total, push(t, w, 0)...)
	if push(t, w, 3); w.Buffered() != 1 {
		t.Fatal("gap must buffer packet 3")
	}
	push(t, w, 2)
	run := push(t, w, 1)
	got := seqs(run)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("contiguous drain = %v, want [1 2 3]", got)
	}
	total = append(total, run...)
	total = append(total, w.Flush()...)
	want := []uint16{0, 1, 2, 3}
	got = seqs(total)
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order broken: %v", got)
		}
	}
}

func TestSequenceWraparoundIsNotNewStream(t *testing.T) {
	w, _ := NewWindow(16)
	if SeqDistance(65535, 0) != 1 {
		t.Fatal("wrap distance 65535->0 must be +1")
	}
	if SeqDistance(0, 65535) != -1 {
		t.Fatal("reverse wrap distance must be -1")
	}
	var total []OrderedPacket
	total = append(total, push(t, w, 65534)...)
	total = append(total, push(t, w, 65535)...)
	// Packet after wrap arrives before its predecessor: it must buffer, and the
	// stream must continue rather than restart.
	push(t, w, 1)
	wrapRun := push(t, w, 0)
	got := seqs(wrapRun)
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("wrap drain = %v, want [0 1]", got)
	}
	total = append(total, wrapRun...)
	// A retransmission of the pre-wrap sequence is a duplicate, not a new flow.
	if out := push(t, w, 65535); len(out) != 0 {
		t.Fatalf("pre-wrap retransmission emitted: %v", seqs(out))
	}
	if w.Stats().Duplicate != 1 {
		t.Fatalf("duplicates = %d, want 1", w.Stats().Duplicate)
	}
	total = append(total, w.Flush()...)
	want := []uint16{65534, 65535, 0, 1}
	got = seqs(total)
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrap ordering broken: got %v want %v", got, want)
		}
	}
}

func TestLatePacketsOutsideWindowDiscarded(t *testing.T) {
	w, _ := NewWindow(4)
	push(t, w, 100) // anchors base=100
	// 110 is 10 ahead, beyond the window of 4: discarded, never buffered.
	if out := push(t, w, 110); len(out) != 0 {
		t.Fatal("out-of-window packet must not be buffered")
	}
	if w.Stats().Late != 1 {
		t.Fatalf("late = %d, want 1", w.Stats().Late)
	}
	if w.Buffered() != 0 {
		t.Fatalf("buffered = %d, want 0", w.Buffered())
	}
	// An in-window forward packet is retained while its hole stays open.
	if out := push(t, w, 102); len(out) != 0 || w.Buffered() != 1 {
		t.Fatalf("in-window packet must buffer: out=%d buf=%d", len(out), w.Buffered())
	}
	// An ancient retransmission beyond the window span is late, not duplicate.
	if out := push(t, w, 90); len(out) != 0 || w.Stats().Late != 2 {
		t.Fatalf("ancient packet handling wrong: stats=%+v", w.Stats())
	}
}

func TestWindowLimitsAndNil(t *testing.T) {
	if _, err := NewWindow(0); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("zero capacity: %v", err)
	}
	if _, err := NewWindow(MaxWindow + 1); timeline.Classify(err) != timeline.KindLimit {
		t.Fatalf("oversized capacity: %v", err)
	}
	w, _ := NewWindow(4)
	if _, err := w.Push(nil, 0); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("nil packet: %v", err)
	}
}

func TestFlushDrainsHolesInRuns(t *testing.T) {
	w, _ := NewWindow(16)
	push(t, w, 0)
	push(t, w, 1)
	push(t, w, 3) // hole at 2
	got := seqs(w.Flush())
	if len(got) != 1 || got[0] != 3 {
		// 0 and 1 already emitted on push; flush only retains 3 (2 is a loss).
		t.Fatalf("flush = %v, want [3]", got)
	}
	if w.Stats().Flushed != 1 {
		t.Fatalf("flushed = %d, want 1", w.Stats().Flushed)
	}
}
