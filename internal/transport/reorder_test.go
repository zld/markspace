package transport

import (
	"testing"

	"github.com/pion/rtp"

	"archivecheck/internal/merr"
)

func mkPkt(seq uint16, ts uint32, payload byte) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, PayloadType: 96},
		Payload: []byte{payload},
	}
}

func seqs(pkts []*rtp.Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.SequenceNumber
	}
	return out
}

func TestInOrder(t *testing.T) {
	b, _ := NewReorderBuffer(4, 1, 1)
	var got []*rtp.Packet
	for i := uint16(0); i < 5; i++ {
		out, err := b.Push(mkPkt(i, uint32(i)*100, byte(i)))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
	}
	got = append(got, b.Flush()...)
	if len(got) != 5 || b.LostCount != 0 || b.DupCount != 0 {
		t.Fatalf("got=%v lost=%d dup=%d", seqs(got), b.LostCount, b.DupCount)
	}
}

func TestReorderWithinWindowRecovered(t *testing.T) {
	// Window 4: order 0,1,3,2,4 — all recovered, no losses.
	b, _ := NewReorderBuffer(4, 1, 1)
	order := []uint16{0, 1, 3, 2, 4}
	var got []*rtp.Packet
	for _, s := range order {
		out, _ := b.Push(mkPkt(s, 0, 0))
		got = append(got, out...)
	}
	got = append(got, b.Flush()...)
	if b.LostCount != 0 {
		t.Fatalf("unexpected losses: %d", b.LostCount)
	}
	want := []uint16{0, 1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", seqs(got), want)
	}
	for i := range want {
		if got[i].SequenceNumber != want[i] {
			t.Fatalf("order wrong: got %v want %v", seqs(got), want)
		}
	}
}

func TestBurstLossOutsideWindow(t *testing.T) {
	// Window 2: 0,1, then jump to 4. Sequences 2,3 are older than the
	// window and must be reported as a genuine gap, not filled in.
	b, _ := NewReorderBuffer(2, 1, 1)
	var got []*rtp.Packet
	for _, s := range []uint16{0, 1, 4} {
		out, _ := b.Push(mkPkt(s, 0, 0))
		got = append(got, out...)
	}
	if b.LostCount != 2 {
		t.Fatalf("LostCount = %d, want 2", b.LostCount)
	}
	// Late arrival of the "lost" packets now counts as duplicates/old.
	out, _ := b.Push(mkPkt(2, 0, 0))
	got = append(got, out...)
	if b.DupCount != 1 {
		t.Fatalf("late packet must be counted as duplicate, dup=%d", b.DupCount)
	}
	got = append(got, b.Flush()...)
	if b.Err() == nil {
		t.Fatal("expected locatable transport gap error")
	}
	if me, ok := merr.As(b.Err()); !ok || me.Kind != merr.KindTransport {
		t.Fatalf("error kind wrong: %v", b.Err())
	}
	if len(got) != 3 {
		t.Fatalf("emitted packets = %d, want 3 (0,1,4)", len(got))
	}
}

func TestDuplicatesCountedOnce(t *testing.T) {
	b, _ := NewReorderBuffer(4, 1, 1)
	var got []*rtp.Packet
	for _, s := range []uint16{0, 0, 1, 1, 2} {
		out, _ := b.Push(mkPkt(s, 0, 0))
		got = append(got, out...)
	}
	got = append(got, b.Flush()...)
	if b.DupCount != 2 || len(got) != 3 {
		t.Fatalf("dup=%d emitted=%d", b.DupCount, len(got))
	}
	if b.Received != 3 {
		t.Fatalf("unique received = %d, want 3", b.Received)
	}
}

func TestFlushCountsTrailingGap(t *testing.T) {
	// 0,1,3 within window 4; stream ends without packet 2 arriving.
	b, _ := NewReorderBuffer(4, 1, 1)
	var got []*rtp.Packet
	for _, s := range []uint16{0, 1, 3} {
		out, _ := b.Push(mkPkt(s, 0, 0))
		got = append(got, out...)
	}
	if b.LostCount != 0 {
		t.Fatalf("gap should stay open until flush, lost=%d", b.LostCount)
	}
	got = append(got, b.Flush()...)
	if b.LostCount != 1 || len(got) != 3 {
		t.Fatalf("after flush lost=%d emitted=%d", b.LostCount, len(got))
	}
	if seqs(got)[2] != 3 {
		t.Fatalf("packet 3 should still be delivered after the gap: %v", seqs(got))
	}
}

func TestUnwrapSequence(t *testing.T) {
	cases := []struct {
		high uint32
		seq  uint16
		want uint32
	}{
		{0, 10, 10},
		{65534, 65535, 65535},
		{65535, 0, 65536},
		{65535, 1, 65537},
		{65540, 65530, 65530}, // backwards jump inside 16-bit range
	}
	for _, c := range cases {
		if got := UnwrapSeq(c.high, c.seq); got != c.want {
			t.Errorf("UnwrapSeq(%d,%d)=%d want %d", c.high, c.seq, got, c.want)
		}
	}
}

func TestUnmarshalEnvelope(t *testing.T) {
	pkt := mkPkt(7, 1234, 42)
	buf, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	env, err := UnmarshalEnvelope(buf, 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	if env.TrackID != 3 || env.SegmentSeq != 9 ||
		env.Packet.SequenceNumber != 7 || env.Packet.Timestamp != 1234 {
		t.Fatalf("bad envelope: %+v", env)
	}
	if _, err := UnmarshalEnvelope([]byte{0x00}, 1, 1); err == nil {
		t.Fatal("malformed datagram must produce a decode error")
	}
}

func TestInvalidWindow(t *testing.T) {
	if _, err := NewReorderBuffer(0, 1, 1); err == nil {
		t.Fatal("window 0 must be rejected")
	}
}
