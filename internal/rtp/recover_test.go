package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{SequenceNumber: seq}}
}

func seqs(pkts []*rtp.Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.SequenceNumber
	}
	return out
}

func TestInOrder(t *testing.T) {
	r := NewRecoverer(8)
	for s := uint16(100); s < 105; s++ {
		out := r.Push(pkt(s))
		if len(out) != 1 || out[0].SequenceNumber != s {
			t.Fatalf("seq %d: out=%v", s, seqs(out))
		}
	}
	st := r.Stats()
	if st.Lost != 0 || st.Recovered != 0 || st.Emitted != 5 {
		t.Fatalf("stats=%+v", st)
	}
	if st.RecoveryRate() != 1 {
		t.Fatalf("rate=%v", st.RecoveryRate())
	}
}

func TestReorderWithinWindow(t *testing.T) {
	r := NewRecoverer(8)
	var got []uint16
	// 0, 2, 1, 3 — packet 1 arrives late but inside the window.
	for _, s := range []uint16{0, 2, 1, 3} {
		got = append(got, seqs(r.Push(pkt(s)))...)
	}
	want := []uint16{0, 1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	st := r.Stats()
	if st.Recovered != 1 || st.Lost != 0 {
		t.Fatalf("stats=%+v", st)
	}
}

func TestDuplicates(t *testing.T) {
	r := NewRecoverer(8)
	r.Push(pkt(0))
	r.Push(pkt(0)) // exact duplicate
	r.Push(pkt(1))
	r.Push(pkt(0)) // already emitted
	if st := r.Stats(); st.Duplicates != 2 {
		t.Fatalf("dups=%d", st.Duplicates)
	}
}

func TestWrapAround(t *testing.T) {
	r := NewRecoverer(8)
	var got []uint16
	for _, s := range []uint16{65534, 65535, 0, 1} {
		got = append(got, seqs(r.Push(pkt(s)))...)
	}
	want := []uint16{65534, 65535, 0, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if st := r.Stats(); st.Lost != 0 {
		t.Fatalf("lost=%d", st.Lost)
	}
}

func TestLossBeyondWindow(t *testing.T) {
	r := NewRecoverer(4)
	r.Push(pkt(0))
	// Jump 10 ahead with window 4: seqs 1..9 declared lost.
	out := r.Push(pkt(10))
	if len(out) != 1 || out[0].SequenceNumber != 10 {
		t.Fatalf("out=%v", seqs(out))
	}
	st := r.Stats()
	if st.Lost != 9 {
		t.Fatalf("lost=%d want 9", st.Lost)
	}
	if st.RecoveryRate() != 0 {
		t.Fatalf("rate=%v", st.RecoveryRate())
	}
}

func TestGradualLossExceedsWindow(t *testing.T) {
	r := NewRecoverer(3)
	var got []uint16
	got = append(got, seqs(r.Push(pkt(0)))...)
	got = append(got, seqs(r.Push(pkt(2)))...) // hole at 1
	got = append(got, seqs(r.Push(pkt(4)))...) // hole at 3, span exceeds window
	// Window 3 forces seq 1 to be declared lost and 2 emitted.
	st := r.Stats()
	if st.Lost != 1 {
		t.Fatalf("lost=%d want 1", st.Lost)
	}
	got = append(got, seqs(r.Flush())...)
	st = r.Stats()
	want := []uint16{0, 2, 4}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if st.Lost != 2 { // holes at 1 (window) and 3 (flush)
		t.Fatalf("lost=%d want 2", st.Lost)
	}
}

func TestRecoveryRate(t *testing.T) {
	r := NewRecoverer(16)
	// 10% loss, each drop retransmitted 3 packets later (inside window).
	dropped := map[uint16]bool{}
	for s := uint16(0); s < 100; s++ {
		if s%10 == 5 {
			dropped[s] = true
		}
	}
	var pending []uint16
	for s := uint16(0); s < 100; s++ {
		if dropped[s] {
			pending = append(pending, s)
			continue
		}
		r.Push(pkt(s))
		var left []uint16
		for _, d := range pending {
			if s-d >= 3 {
				r.Push(pkt(d)) // retransmission
			} else {
				left = append(left, d)
			}
		}
		pending = left
	}
	for _, d := range pending {
		r.Push(pkt(d))
	}
	r.Flush()
	st := r.Stats()
	if st.Recovered != 10 || st.Lost != 0 {
		t.Fatalf("stats=%+v", st)
	}
	if st.RecoveryRate() != 1 {
		t.Fatalf("rate=%v", st.RecoveryRate())
	}
}
