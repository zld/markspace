package rtp

import (
	"math"
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16, marker byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 97, SequenceNumber: seq,
			Timestamp: uint32(seq) * 480, SSRC: 42, Marker: marker == 1,
		},
		Payload: []byte{byte(seq)},
	}
}

func seqs(pkts []*rtp.Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.SequenceNumber
	}
	return out
}

func TestReorder_InOrderAndReorder(t *testing.T) {
	b := NewReorderBuffer(4)
	var got []uint16
	for _, seq := range []uint16{0, 1, 2, 3} {
		ready, dup := b.Feed(pkt(seq, 0))
		if dup {
			t.Fatalf("seq %d flagged duplicate", seq)
		}
		got = append(got, seqs(ready)...)
	}
	if len(got) != 4 || got[3] != 3 {
		t.Fatalf("in-order delivery = %v", got)
	}
	// 5 arrives before 4: nothing delivered until 4 fills the gap.
	if ready, _ := b.Feed(pkt(5, 0)); len(ready) != 0 {
		t.Fatalf("gap must hold delivery, got %v", seqs(ready))
	}
	ready, _ := b.Feed(pkt(4, 0))
	if seqs(ready); len(ready) != 2 || ready[0].SequenceNumber != 4 {
		t.Fatalf("recovered delivery = %v", seqs(ready))
	}
	s := b.Stats()
	if s.Recovered != 1 || s.Lost != 0 || s.Duplicates != 0 || s.Late != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if r := b.LossRecoveryRate(); r != 1 {
		t.Fatalf("recovery rate = %v, want 1", r)
	}
}

func TestReorder_Duplicates(t *testing.T) {
	b := NewReorderBuffer(4)
	b.Feed(pkt(10, 0))
	if _, dup := b.Feed(pkt(10, 0)); !dup {
		t.Fatal("repeated sequence must be a duplicate")
	}
	if b.Stats().Duplicates != 1 || b.Stats().Received != 1 {
		t.Fatalf("duplicate accounting wrong: %+v", b.Stats())
	}
}

func TestReorder_WindowLossAndLate(t *testing.T) {
	b := NewReorderBuffer(3)
	b.Feed(pkt(0, 0))
	b.Feed(pkt(1, 0))
	b.Feed(pkt(2, 0))
	// Missing seq 3; advancing the frontier by the window declares it lost.
	ready, _ := b.Feed(pkt(6, 0))
	if b.Stats().Lost != 1 {
		t.Fatalf("expected 1 loss, got %+v", b.Stats())
	}
	// The late copy of 3 must not fabricate or re-enter ordering.
	if _, dup := b.Feed(pkt(3, 0)); dup {
		t.Fatal("late packet is not a duplicate")
	}
	if b.Stats().Late != 1 {
		t.Fatalf("late accounting wrong: %+v", b.Stats())
	}
	// No stand-in packet for the lost slot was ever produced.
	for _, p := range ready {
		if p.SequenceNumber == 3 {
			t.Fatal("lost slot must never be fabricated")
		}
	}
	if r := b.LossRecoveryRate(); r >= 1 {
		t.Fatalf("recovery rate must drop below 1 after a loss: %v", r)
	}
}

func TestReorder_SequenceWrap(t *testing.T) {
	b := NewReorderBuffer(4)
	b.Feed(pkt(65534, 0))
	b.Feed(pkt(65535, 0))
	ready, _ := b.Feed(pkt(0, 0))
	if len(ready) != 1 || ready[0].SequenceNumber != 0 {
		t.Fatalf("wrap delivery failed: %v", seqs(ready))
	}
	if b.Stats().Lost != 0 {
		t.Fatalf("natural wrap must not count as loss: %+v", b.Stats())
	}
}

func TestReorder_FlushAndRate(t *testing.T) {
	b := NewReorderBuffer(8)
	b.Feed(pkt(0, 0)) // delivered immediately, frontier next=1
	b.Feed(pkt(2, 0)) // gap 1 left behind; 2 stays buffered as a reorder slot
	out := b.Flush()
	if seqs(out); len(out) != 1 || out[0].SequenceNumber != 2 {
		t.Fatalf("flush = %v", seqs(out))
	}
	s := b.Stats()
	if s.Lost != 1 || s.Recovered != 1 {
		t.Fatalf("flush stats = %+v", s)
	}
	want := 0.5
	if math.Abs(b.LossRecoveryRate()-want) > 1e-9 {
		t.Fatalf("rate = %v, want %v", b.LossRecoveryRate(), want)
	}
}

func TestReorder_EmptyFlush(t *testing.T) {
	b := NewReorderBuffer(2)
	if out := b.Flush(); out != nil {
		t.Fatalf("empty stream flush must be nil, got %v", out)
	}
	if r := b.LossRecoveryRate(); r != 1 {
		t.Fatalf("clean stream rate = %v, want 1", r)
	}
}
