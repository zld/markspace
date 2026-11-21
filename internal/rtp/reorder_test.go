package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16, payloadLen int) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			SSRC:           0x12345678,
		},
		Payload: make([]byte, payloadLen),
	}
}

func feed(t *testing.T, r *Reorderer, pkts ...*rtp.Packet) int {
	t.Helper()
	delivered := 0
	for _, p := range pkts {
		got, err := r.Process(p)
		if err != nil {
			t.Fatalf("unexpected error for seq %d: %v", p.SequenceNumber, err)
		}
		delivered += len(got)
	}
	return delivered
}

func TestInOrderDelivery(t *testing.T) {
	r := NewReorderer(DefaultWindowSize, 1400)
	got := feed(t, r, pkt(10, 10), pkt(11, 10), pkt(12, 10))
	if got != 3 {
		t.Fatalf("delivered %d, want 3", got)
	}
	if s := r.Stats(); s.SequenceGaps != 0 || s.DuplicatePackets != 0 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestOutOfOrderWithinWindow(t *testing.T) {
	r := NewReorderer(4, 1400)
	// 0, then 2 and 3 buffered, 1 arrives last and the chain drains.
	got := feed(t, r, pkt(0, 5), pkt(2, 5), pkt(3, 5), pkt(1, 5))
	if got != 4 {
		t.Fatalf("delivered %d, want 4", got)
	}
	if s := r.Stats(); s.SequenceGaps != 0 {
		t.Fatalf("want no gaps, got %+v", s)
	}
}

func TestDuplicatesDoNotCountGaps(t *testing.T) {
	r := NewReorderer(4, 1400)
	p := pkt(7, 5)
	feed(t, r, p)
	if _, err := r.Process(p); err != nil {
		t.Fatal(err)
	}
	// a late copy of an already delivered packet is also a duplicate
	if _, err := r.Process(pkt(6, 5)); err != nil {
		t.Fatal(err)
	}
	s := r.Stats()
	if s.DuplicatePackets != 2 || s.SequenceGaps != 0 {
		t.Fatalf("want 2 duplicates and 0 gaps, got %+v", s)
	}
}

func TestWindowExhaustionConfirmsGap(t *testing.T) {
	r := NewReorderer(2, 1400)
	// seq 10 delivered; 12 buffered (offset 1, inside the window);
	// seq 13 reaches offset 2 and exhausts the window, so seq 11 is
	// confirmed missing and the buffered 12,13 chain is delivered.
	feed(t, r, pkt(10, 5))
	if got, _ := r.Process(pkt(12, 5)); len(got) != 0 {
		t.Fatalf("seq 12 must be buffered, got %+v", got)
	}
	got, err := r.Process(pkt(13, 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SequenceNumber != 12 || got[1].SequenceNumber != 13 {
		t.Fatalf("want 12,13 delivered in order, got %+v", got)
	}
	s := r.Stats()
	if s.SequenceGaps != 1 {
		t.Fatalf("want 1 confirmed gap, got %d", s.SequenceGaps)
	}
	// the missing packet arriving late is a duplicate, not a new gap
	feed(t, r, pkt(11, 5))
	if r.Stats().SequenceGaps != 1 || r.Stats().DuplicatePackets != 1 {
		t.Fatalf("late packet must stay a duplicate: %+v", r.Stats())
	}
}

func TestSequenceWrapAround(t *testing.T) {
	r := NewReorderer(4, 1400)
	var delivered []uint16
	for _, seq := range []uint16{65534, 65535, 0, 1, 2} {
		got, err := r.Process(pkt(seq, 5))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range got {
			delivered = append(delivered, p.SequenceNumber)
		}
	}
	want := []uint16{65534, 65535, 0, 1, 2}
	if len(delivered) != len(want) {
		t.Fatalf("want %d in-order packets across wrap, got %d (%v)", len(want), len(delivered), delivered)
	}
	for i, seq := range want {
		if delivered[i] != seq {
			t.Fatalf("position %d: got seq %d want %d", i, delivered[i], seq)
		}
	}
	if r.Stats().SequenceGaps != 0 {
		t.Fatalf("wrap must not create gaps: %+v", r.Stats())
	}
}

func TestFlushReportsTrailingGap(t *testing.T) {
	r := NewReorderer(8, 1400)
	// seq 0 delivered, seq 1 missing, seq 2 buffered.
	feed(t, r, pkt(0, 5))
	if got, _ := r.Process(pkt(2, 5)); len(got) != 0 {
		t.Fatalf("seq 2 must wait for the missing seq 1, got %+v", got)
	}
	// flush confirms seq 1 as a gap and delivers the buffered seq 2.
	out := r.Flush()
	if len(out) != 1 || out[0].SequenceNumber != 2 {
		t.Fatalf("flush should deliver buffered seq 2, got %+v", out)
	}
	if r.Stats().SequenceGaps != 1 {
		t.Fatalf("flush must confirm seq 1 missing: %+v", r.Stats())
	}
}

func TestMalformedPacketsRejected(t *testing.T) {
	r := NewReorderer(4, 1400)
	cases := []*rtp.Packet{
		{Header: rtp.Header{Version: 1}, Payload: []byte{1}},
		{Header: rtp.Header{Version: 2, Extension: true}, Payload: []byte{1}},
		{Header: rtp.Header{Version: 2}, Payload: []byte{1}, PaddingSize: 5},
		{Header: rtp.Header{Version: 2}, Payload: nil},
		{Header: rtp.Header{Version: 2}, Payload: make([]byte, 1401)},
	}
	for i, p := range cases {
		if _, err := r.Process(p); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
	if r.Stats().MalformedPackets != len(cases) {
		t.Fatalf("want %d malformed, got %+v", len(cases), r.Stats())
	}
}

func TestParseTruncated(t *testing.T) {
	if _, err := Parse([]byte{0x80, 0x60}, 1400); err == nil {
		t.Fatal("truncated datagram must be rejected")
	}
}
