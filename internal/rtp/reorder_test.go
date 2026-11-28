package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"medcheck/internal/model"
)

func packet(seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, SSRC: 42, PayloadType: 96},
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

func eq(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestInWindowReorder(t *testing.T) {
	r := NewReorderer(1, 7, 4)
	var got []*rtp.Packet
	for _, s := range []uint16{0, 2, 1, 4, 3} {
		got = append(got, r.Push(packet(s)).Packets...)
	}
	got = append(got, r.Flush().Packets...)
	if !eq(seqs(got), []uint16{0, 1, 2, 3, 4}) {
		t.Fatalf("order = %v", seqs(got))
	}
}

func TestDuplicatesDropped(t *testing.T) {
	r := NewReorderer(1, 7, 4)
	res1 := r.Push(packet(5))
	res2 := r.Push(packet(5))
	if res2.Duplicates != 1 || len(res2.Packets) != 0 {
		t.Fatalf("dup result = %+v", res2)
	}
	if len(res1.Packets) != 1 {
		t.Fatalf("first push = %+v", res1)
	}
}

func TestOutOfWindowGap(t *testing.T) {
	r := NewReorderer(1, 7, 2)
	res := r.Push(packet(0))
	if len(res.Packets) != 1 || len(res.Gaps) != 0 {
		t.Fatalf("push 0 = %+v", res)
	}
	// seq 4 is 4 ahead, window 2 -> 1,2,3 finalized as gaps.
	res = r.Push(packet(4))
	if len(res.Gaps) != 3 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	want := []uint16{1, 2, 3}
	for i, g := range res.Gaps {
		if g.Seq != want[i] {
			t.Fatalf("gap %d = %d want %d", i, g.Seq, want[i])
		}
	}
	if len(res.Packets) != 1 || res.Packets[0].SequenceNumber != 4 {
		t.Fatalf("packet 4 not emitted: %+v", res.Packets)
	}
}

func TestLatePacketOutsideWindowDropped(t *testing.T) {
	r := NewReorderer(1, 7, 2)
	r.Push(packet(0))
	r.Push(packet(4)) // forces expected to 4
	res := r.Push(packet(1))
	if res.Duplicates != 1 || len(res.Packets) != 0 {
		t.Fatalf("late packet result = %+v", res)
	}
}

func TestFlushReportsTrailingHoles(t *testing.T) {
	r := NewReorderer(1, 7, 10)
	first := r.Push(packet(10)) // 10 is emitted immediately
	r.Push(packet(12))          // 12 buffered behind missing 11
	res := r.Flush()
	if len(res.Gaps) != 1 || res.Gaps[0].Seq != 11 {
		t.Fatalf("flush gaps = %+v", res.Gaps)
	}
	if len(first.Packets) != 1 || first.Packets[0].SequenceNumber != 10 {
		t.Fatalf("first push = %+v", seqs(first.Packets))
	}
	if len(res.Packets) != 1 || res.Packets[0].SequenceNumber != 12 {
		t.Fatalf("flush packets = %+v", seqs(res.Packets))
	}
}

func TestSequenceWrap(t *testing.T) {
	r := NewReorderer(1, 7, 3)
	var got []*rtp.Packet
	for _, s := range []uint16{65534, 65535, 0, 1} {
		got = append(got, r.Push(packet(s)).Packets...)
	}
	if !eq(seqs(got), []uint16{65534, 65535, 0, 1}) {
		t.Fatalf("wrap order = %v", seqs(got))
	}
}

func TestGapErrorModel(t *testing.T) {
	ce := Gap{Track: 1, Source: 7, Seq: 9}.GapError(123)
	if ce.Kind != model.KindGap || ce.FirstPos != 123 || ce.Seq != 9 {
		t.Fatalf("gap error = %+v", ce)
	}
}

func TestUnmarshalRealRTPBytes(t *testing.T) {
	raw, err := packet(3).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 3 || p.SSRC != 42 {
		t.Fatalf("parsed = %+v", p)
	}
}
