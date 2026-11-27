package reorder

import (
	"testing"

	"medialib/internal/ingest"
)

func parseAll(t *testing.T, spec ingest.StreamSpec) []ingest.Packet {
	t.Helper()
	raw := ingest.GenRTP(spec)
	pkts := make([]ingest.Packet, 0, len(raw))
	for i, b := range raw {
		p, err := ingest.ParseRTP(b, i)
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
	}
	return pkts
}

func TestDedupAndGaps(t *testing.T) {
	spec := ingest.StreamSpec{
		ClockRate: 32000, Packets: 12, BaseSeq: 100, BaseTS: 0,
		FrameSamples: []uint32{640}, DupSeq: []int{4}, DropSeq: []int{6, 7}, Seed: 9,
	}
	res, err := Window{Size: 4}.Process(parseAll(t, spec),
		func(seq uint32) int64 { return spec.FrameDur(int(seq)) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicates != 1 {
		t.Fatalf("duplicates=%d", res.Duplicates)
	}
	if res.LostPackets != 2 {
		t.Fatalf("lost=%d", res.LostPackets)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps=%d", len(res.Gaps))
	}
	g := res.Gaps[0]
	if g.Count != 2 || g.Samples != 1280 || g.StartSample != 6*640 {
		t.Fatalf("gap=%+v", g)
	}
	// Ordered output must be strictly increasing in sequence.
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].Seq <= res.Ordered[i-1].Seq {
			t.Fatal("output not ordered")
		}
	}
}

func TestConflictingDuplicate(t *testing.T) {
	spec := ingest.StreamSpec{
		ClockRate: 32000, Packets: 4, BaseSeq: 1, BaseTS: 0,
		FrameSamples: []uint32{640}, Seed: 1,
	}
	pkts := parseAll(t, spec)
	bad := pkts[1]
	bad.Payload = append([]byte(nil), bad.Payload...)
	bad.Payload[0] ^= 0xff // same seq, different content
	pkts = append(pkts, bad)
	if _, err := (Window{Size: 4}).Process(pkts,
		func(seq uint32) int64 { return 640 }); err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestWindowOverflow(t *testing.T) {
	spec := ingest.StreamSpec{
		ClockRate: 32000, Packets: 30, BaseSeq: 1, BaseTS: 0,
		FrameSamples: []uint32{640}, Seed: 1,
	}
	pkts := parseAll(t, spec)
	// GenRTP reverses bursts of 3: depth 2. A window of 1 must fail.
	if _, err := (Window{Size: 1}).Process(pkts,
		func(seq uint32) int64 { return 640 }); err == nil {
		t.Fatal("expected window overflow")
	}
}
