package rtp

import (
	"testing"

	prtp "github.com/pion/rtp"
)

func mkPacket(seq uint16) *prtp.Packet {
	return &prtp.Packet{
		Header:  prtp.Header{SequenceNumber: seq, SSRC: 1, Timestamp: uint32(seq) * 160},
		Payload: []byte{byte(seq >> 8), byte(seq)},
	}
}

func push(t *testing.T, b *JitterBuffer, seq uint16) {
	t.Helper()
	if err := b.Push(FromPion(mkPacket(seq))); err != nil {
		t.Fatalf("push seq=%d: %v", seq, err)
	}
}

func seqs(pkts []Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.Seq
	}
	return out
}

func TestReorderAndDuplicate(t *testing.T) {
	b := NewJitterBuffer(4)
	for _, s := range []uint16{0, 1, 3, 2, 4} { // 3 before 2
		push(t, b, s)
	}
	// The duplicate of seq 2 must be counted and rejected.
	err := b.Push(FromPion(mkPacket(2)))
	if err == nil {
		t.Fatal("duplicate must return a classified error")
	}
	out := b.Flush()
	got := seqs(out)
	want := []uint16{0, 1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("reordered=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reordered=%v want %v", got, want)
		}
	}
	st := b.Stats()
	if st.Duplicates != 1 || st.Received != 5 {
		t.Fatalf("dup stats wrong: %+v", st)
	}
}

func TestRecoveryInsideWindow(t *testing.T) {
	b := NewJitterBuffer(5)
	push(t, b, 10)
	push(t, b, 11)
	// seq 12 missing for now; push ahead to open the gap.
	push(t, b, 13)
	push(t, b, 14)
	if out := b.Flush(); len(out) != 2 { // 10,11 released; 12 gap waits
		t.Fatalf("expected hold at gap, got %d", len(out))
	}
	// Late arrival inside the window fills the gap.
	push(t, b, 12)
	out := b.Flush()
	if len(out) != 3 || seqs(out)[0] != 12 {
		t.Fatalf("recovered flush=%v", seqs(out))
	}
	st := b.Stats()
	if st.Recovered != 1 || st.Loss != 0 {
		t.Fatalf("recovery stats wrong: %+v", st)
	}
	if rate := b.RecoveryRatePercent(); rate != 100 {
		t.Fatalf("recovery rate=%v want 100", rate)
	}
}

func TestLossOutsideWindow(t *testing.T) {
	b := NewJitterBuffer(2)
	push(t, b, 0)
	push(t, b, 1)
	// seq 2,3 lost; 4..7 arrive, exceeding the window distance.
	for _, s := range []uint16{4, 5, 6, 7} {
		push(t, b, s)
	}
	// First flush releases the contiguous head (0,1); the gap at 2,3 is only
	// declared once its distance exceeds the window on the following flush.
	first := b.Flush()
	if len(first) != 2 || seqs(first)[0] != 0 || seqs(first)[1] != 1 {
		t.Fatalf("head flush=%v", seqs(first))
	}
	out := b.Flush()
	if len(out) != 4 || seqs(out)[0] != 4 {
		t.Fatalf("post-loss flush=%v", seqs(out))
	}
	st := b.Stats()
	if st.Loss != 2 || st.MaxGap != 2 {
		t.Fatalf("loss stats wrong: %+v", st)
	}
	if len(st.LossRanges) != 1 || st.LossRanges[0].From != 2 || st.LossRanges[0].To != 3 {
		t.Fatalf("loss range wrong: %+v", st.LossRanges)
	}
	// A late packet for an already-declared loss is a duplicate, not media.
	err := b.Push(FromPion(mkPacket(2)))
	if err == nil {
		t.Fatal("late post-loss packet must be rejected")
	}
	if b.Stats().Duplicates != 1 {
		t.Fatal("post-loss late packet must increment duplicates")
	}
	if rate := b.RecoveryRatePercent(); rate < 0 || rate > 100 {
		t.Fatalf("recovery rate out of range: %v", rate)
	}
}

func TestSequenceWrapAndDrain(t *testing.T) {
	b := NewJitterBuffer(3)
	// Approach the 16-bit boundary, wrap, and reorder across it.
	for _, s := range []uint16{0xFFFE, 0xFFFF, 1, 0} {
		push(t, b, s)
	}
	out := b.Flush()
	if len(out) != 4 {
		t.Fatalf("wrap reorder held=%d", len(out))
	}
	want := []uint16{0xFFFE, 0xFFFF, 0, 1}
	for i, p := range out {
		if p.Seq != want[i] {
			t.Fatalf("wrap order=%v want %v", seqs(out), want)
		}
	}
	// Drain at stream end declares the trailing gap.
	push(t, b, 5)
	drained := b.Drain()
	if len(drained) != 1 || drained[0].Seq != 5 {
		t.Fatalf("drain=%v", seqs(drained))
	}
	st := b.Stats()
	if st.Loss != 3 { // extended seq 2,3,4 missing
		t.Fatalf("drain loss=%d want 3 ranges=%+v", st.Loss, st.LossRanges)
	}
}

func TestNoFabricationAndP95(t *testing.T) {
	b := NewJitterBuffer(2)
	push(t, b, 0)
	push(t, b, 3) // gap 1,2 beyond window
	b.Flush()
	// Every released packet must carry real, non-empty payload.
	for _, p := range b.Drain() {
		if len(p.Payload) == 0 {
			t.Fatal("buffer must never fabricate media samples")
		}
	}
	if st := b.Stats(); st.P95Latency < 0 {
		t.Fatalf("p95 latency must be non-negative: %d", st.P95Latency)
	}
}
