package fixture

import (
	"bytes"
	"testing"

	"mediacheck/internal/stream"
)

func TestDeterministicAcrossGenerators(t *testing.T) {
	a := NewGenerator(7).Normal(stream.CodecH264, 12, 4)
	b := NewGenerator(7).Normal(stream.CodecH264, 12, 4)
	if len(a.Datagrams) != len(b.Datagrams) {
		t.Fatalf("datagram counts differ: %d vs %d", len(a.Datagrams), len(b.Datagrams))
	}
	for i := range a.Datagrams {
		if !bytes.Equal(a.Datagrams[i].Raw, b.Datagrams[i].Raw) {
			t.Fatalf("datagram %d differs for the same seed", i)
		}
	}
	if c := NewGenerator(8).Normal(stream.CodecH264, 12, 4); bytes.Equal(
		a.Datagrams[0].Raw, c.Datagrams[0].Raw) {
		t.Fatal("different seeds must produce different streams")
	}
}

func TestNormalStreamShape(t *testing.T) {
	s := NewGenerator(7).Normal(stream.CodecH264, 12, 4)
	if s.SampleCount != 12 || len(s.RTCP) != 1 {
		t.Fatalf("normal stream shape wrong: %+v", s)
	}
	if !s.Packets[0].Marker {
		// Single-NAL frames: the first packet of a frame is also last.
		t.Fatal("first frame packet should carry the marker")
	}
	keys := 0
	for _, p := range s.Packets {
		if p.Marker && p.Payload[0]&0x1F == 5 {
			keys++
		}
	}
	if keys != 3 { // frames 0,4,8
		t.Fatalf("IDR count = %d, want 3", keys)
	}
}

func TestReorderLossAndTruncation(t *testing.T) {
	g := NewGenerator(7)
	s := g.Normal(stream.CodecH264, 10, 5)
	before := len(s.Packets)
	s.ReorderAndLoss(3, 2, 5)
	if len(s.Packets) != before-2 {
		t.Fatalf("drop count wrong: %d -> %d", before, len(s.Packets))
	}
	// Reordering must not change the seq set, only arrival order.
	seqs := map[uint16]bool{}
	for _, p := range s.Packets {
		if seqs[p.SequenceNumber] {
			t.Fatal("duplicate seq after reorder")
		}
		seqs[p.SequenceNumber] = true
	}

	tr := g.Normal(stream.CodecH264, 10, 5)
	target := tr.Packets[3].SequenceNumber
	origLen := len(tr.Packets[3].Payload)
	tr.TruncateNAL(target, 1)
	for _, p := range tr.Packets {
		if p.SequenceNumber == target && len(p.Payload) != 1 {
			t.Fatalf("truncation failed: %d vs 1 (orig %d)", len(p.Payload), origLen)
		}
	}
}

func TestH265FragmentationPath(t *testing.T) {
	g := NewGenerator(7)
	// Force fragmentation with a large synthetic NAL via Normal plus a
	// direct packetize call through a frame larger than the MTU.
	big := make([][]byte, 1)
	nal := append([]byte{0x26}, make([]byte, 3000)...)
	big[0] = nal
	var seq uint16
	pkts := g.packetize(stream.CodecH265, big, 5000, &seq)
	if len(pkts) < 3 || !pkts[len(pkts)-1].Marker {
		t.Fatalf("fragmentation shape wrong: %d packets", len(pkts))
	}
	if pkts[0].Payload[2]&0x80 == 0 {
		t.Fatal("first HEVC FU must set the start bit")
	}
}

func TestPayloadAndHeaderHashesDiffer(t *testing.T) {
	s := NewGenerator(7).Normal(stream.CodecH264, 8, 4)
	if bytes.Equal(PayloadBytes(s.Packets)[0], HeaderBytes(s.Packets)[0]) {
		t.Fatal("payload and header identity inputs must differ")
	}
}
