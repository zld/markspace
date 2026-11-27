package fixture

import (
	"testing"

	"github.com/pion/rtp"
)

func parse(t *testing.T, d Datagram) *rtp.Packet {
	t.Helper()
	p := &rtp.Packet{}
	if err := p.Unmarshal(d.Data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p
}

func TestBuildClean(t *testing.T) {
	dgs, err := Build(Spec{Seed: 1, Kind: KindClean, Codec: "opus", Rate: 48000, PacketCount: 8, PayloadSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(dgs) != 8 {
		t.Fatalf("datagrams = %d, want 8", len(dgs))
	}
	// Offsets must be strictly increasing and reflect real byte positions.
	for i := 1; i < len(dgs); i++ {
		if dgs[i].Offset <= dgs[i-1].Offset {
			t.Fatalf("offset not increasing at %d", i)
		}
	}
	first := parse(t, dgs[0])
	if first.SequenceNumber != 100 {
		t.Fatalf("first seq = %d, want 100", first.SequenceNumber)
	}
}

func TestBuildLossAndDuplicate(t *testing.T) {
	dgs, _ := Build(Spec{Seed: 2, Kind: KindLossDup, Codec: "opus", Rate: 48000, PacketCount: 8, PayloadSize: 16})
	// 8 packets minus 1 dropped, plus 1 duplicate = 8 datagrams.
	if len(dgs) != 8 {
		t.Fatalf("datagrams = %d, want 8", len(dgs))
	}
	seen := map[uint16]int{}
	for _, d := range dgs {
		seen[parse(t, d).SequenceNumber]++
	}
	if seen[103] != 0 {
		t.Fatal("seq 103 should have been dropped")
	}
	if seen[105] != 2 {
		t.Fatalf("seq 105 count = %d, want 2 (duplicate)", seen[105])
	}
}

func TestBuildTruncated(t *testing.T) {
	full, _ := Build(Spec{Seed: 3, Kind: KindClean, Codec: "aac", Rate: 48000, PacketCount: 6, PayloadSize: 30})
	trunc, _ := Build(Spec{Seed: 3, Kind: KindTruncated, Codec: "aac", Rate: 48000, PacketCount: 6, PayloadSize: 30})
	// Same number of datagrams, but index 4 carries a shorter payload.
	if len(trunc) != len(full) {
		t.Fatalf("counts differ: %d vs %d", len(trunc), len(full))
	}
	shortPkt := parse(t, trunc[4])
	normalPkt := parse(t, full[4])
	if len(shortPkt.Payload) >= len(normalPkt.Payload) {
		t.Fatalf("truncated payload %d not shorter than %d",
			len(shortPkt.Payload), len(normalPkt.Payload))
	}
}

func TestBuildAACHeaderValid(t *testing.T) {
	dgs, _ := Build(Spec{Seed: 4, Kind: KindClean, Codec: "aac", Rate: 48000, PacketCount: 3, PayloadSize: 24})
	p := parse(t, dgs[0])
	if p.Payload[0] != 0xFF || p.Payload[1]&0xF0 != 0xF0 {
		t.Fatalf("bad ADTS sync: %x", p.Payload[:2])
	}
}
