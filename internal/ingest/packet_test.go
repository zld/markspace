package ingest

import (
	"bytes"
	"testing"

	"medialab/internal/model"
)

func packetsFor(seqs []uint32, track model.TrackID) []Packet {
	out := make([]Packet, len(seqs))
	for i, s := range seqs {
		out[i] = Packet{Track: track, Seq: s, Frame: i / 3, Parts: 1, Part: 0,
			Data: []byte{byte(i)}, Marker: true}
	}
	return out
}

func TestExpandSeqsWrap(t *testing.T) {
	// 0xfffd, 0xfffe, 0xffff, 0x0000, 0x0001 crosses the 16-bit boundary.
	exp, err := ExpandSeqs([]uint32{0xfffd, 0xfffe, 0xffff, 0x0000, 0x0001})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{0xfffd, 0xfffe, 0xffff, 0x10000, 0x10001}
	for i, w := range want {
		if exp.Values[i] != w {
			t.Fatalf("value %d = %d want %d", i, exp.Values[i], w)
		}
	}
}

func TestExpandSeqsOutOfOrder(t *testing.T) {
	// A packet arriving two ticks early must land below its predecessor.
	exp, err := ExpandSeqs([]uint32{100, 102, 101, 103})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{100, 102, 101, 103}
	for i, w := range want {
		if exp.Values[i] != w {
			t.Fatalf("value %d = %d want %d", i, exp.Values[i], w)
		}
	}
}

func TestExpandSeqsRejectsIllegal(t *testing.T) {
	if _, err := ExpandSeqs([]uint32{70000}); err == nil {
		t.Fatal("17-bit sequence accepted")
	}
}

func TestReorderWindowRepairsShuffle(t *testing.T) {
	pkts := []Packet{
		{Track: 1, Seq: 10, Frame: 0, Parts: 1, Data: []byte{0}, Marker: true},
		{Track: 1, Seq: 12, Frame: 2, Parts: 1, Data: []byte{2}, Marker: true},
		{Track: 1, Seq: 11, Frame: 1, Parts: 1, Data: []byte{1}, Marker: true},
		{Track: 1, Seq: 13, Frame: 3, Parts: 1, Data: []byte{3}, Marker: true},
	}
	res := Reorder(pkts, ReorderConfig{Window: 4})
	if res.Fatal != nil {
		t.Fatal(res.Fatal)
	}
	if len(res.Packets) != 4 || len(res.Missing) != 0 {
		t.Fatalf("packets=%d missing=%d", len(res.Packets), len(res.Missing))
	}
	for i, p := range res.Packets {
		if p.Frame != i {
			t.Fatalf("packet %d frame = %d", i, p.Frame)
		}
	}
}

func TestReorderMissingBeyondWindow(t *testing.T) {
	var pkts []Packet
	for _, s := range []uint32{1, 2, 9, 10} {
		pkts = append(pkts, Packet{Track: 1, Seq: s, Frame: int(s), Parts: 1,
			Data: []byte{byte(s)}, Marker: true})
	}
	res := Reorder(pkts, ReorderConfig{Window: 2})
	if res.Fatal != nil {
		t.Fatalf("gap beyond window should be recoverable: %v", res.Fatal)
	}
	if len(res.Warnings) == 0 || !res.Warnings[0].Recoverable {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if res.Filled != 6 {
		t.Fatalf("filled = %d want 6", res.Filled)
	}
	if len(res.Missing) != 6 {
		t.Fatalf("missing = %d", len(res.Missing))
	}
}

func TestReorderDeduplicates(t *testing.T) {
	pkts := []Packet{
		{Track: 1, Seq: 5, Frame: 0, Parts: 1, Data: []byte{1}, Marker: true},
		{Track: 1, Seq: 5, Frame: 0, Parts: 1, Data: []byte{1}, Marker: true},
		{Track: 1, Seq: 6, Frame: 1, Parts: 1, Data: []byte{2}, Marker: true},
	}
	res := Reorder(pkts, ReorderConfig{Window: 2})
	if len(res.Packets) != 2 || res.Duplicates != 1 {
		t.Fatalf("packets=%d duplicates=%d", len(res.Packets), res.Duplicates)
	}
}

func TestReorderCorruptPayloadStopsTrack(t *testing.T) {
	pkts := []Packet{
		{Track: 1, Seq: 1, Frame: 0, Parts: 1, Data: []byte{1}, Marker: true},
		{Track: 1, Seq: 2, Frame: 1, Parts: 1, Data: nil, Marker: true},
	}
	res := Reorder(pkts, ReorderConfig{Window: 4})
	if res.Fatal == nil || res.Fatal.Code != model.CodePayloadCorrupt {
		t.Fatalf("fatal = %v", res.Fatal)
	}
	if res.Fatal.Track != 1 {
		t.Fatalf("track = %d", res.Fatal.Track)
	}
}

func TestReorderCacheBound(t *testing.T) {
	pkts := packetsFor([]uint32{1, 2, 3}, 1)
	res := Reorder(pkts, ReorderConfig{Window: 4, MaxPackets: 2})
	if res.Fatal == nil || res.Fatal.Code != model.CodeCacheOverflow {
		t.Fatalf("fatal = %v", res.Fatal)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	p := Packet{Track: 3, Seq: 65535, RTPTs: 9000, Marker: true,
		Frame: 12, Parts: 2, Part: 1, Data: bytes.Repeat([]byte{0xab}, 40)}
	wire, err := MarshalPacket(p)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalPacket(wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.Track != p.Track || back.Seq != p.Seq || back.RTPTs != p.RTPTs ||
		back.Frame != p.Frame || back.Parts != p.Parts || back.Part != p.Part ||
		!bytes.Equal(back.Data, p.Data) {
		t.Fatalf("round trip = %+v", back)
	}
	if _, err := UnmarshalPacket([]byte{0x80}); err == nil {
		t.Fatal("short packet accepted")
	}
}

func TestAssembleFrames(t *testing.T) {
	pkts := []Packet{
		{Track: 1, Seq: 1, Frame: 0, Parts: 2, Part: 0, Data: []byte("ab")},
		{Track: 1, Seq: 2, Frame: 0, Parts: 2, Part: 1, Data: []byte("cd"), Marker: true},
		{Track: 1, Seq: 3, Frame: 1, Parts: 2, Part: 0, Data: []byte("ef")},
		{Track: 1, Seq: 4, Frame: 1, Parts: 2, Part: 1, Data: []byte("gh"), Marker: true},
	}
	out, warns := Assemble(pkts)
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	if string(out[0]) != "abcd" || string(out[1]) != "efgh" {
		t.Fatalf("assembled = %q %q", out[0], out[1])
	}

	out, warns = Assemble(pkts[:3])
	if len(out) != 1 || len(warns) != 1 || !warns[0].Recoverable {
		t.Fatalf("partial assembly: out=%v warns=%v", out, warns)
	}
}
