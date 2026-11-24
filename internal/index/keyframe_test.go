package index

import (
	"testing"

	"github.com/example/tsrepair/internal/ts"
)

func keyframePacket(pid uint16, pusi, random bool, offset int64) *ts.Packet {
	control := byte(1)
	if random {
		control = 3
	}
	raw := ts.BuildPacket(ts.BuildOpts{
		PID: pid, PUSI: pusi, AdaptationControl: control,
		RandomAccess: random, CC: 1, Payload: []byte("frame")})
	p, err := ts.Parse(raw, offset)
	if err != nil {
		panic(err)
	}
	return p
}

func TestBuilderAdmitsOnlyVerifiableKeyframes(t *testing.T) {
	b := NewBuilder()
	cases := []struct {
		pusi, random bool
		want         bool
	}{
		{true, true, true},
		{false, true, false}, // no unit start
		{true, false, false}, // no random-access indicator
	}
	for i, c := range cases {
		p := keyframePacket(0x100, c.pusi, c.random, int64(i)*188)
		res := b.Consider(p, int64(i), uint64(90000*i))
		if res.Admitted != c.want {
			t.Fatalf("case %d: admitted=%v want %v reason=%s", i, res.Admitted, c.want, res.Reason)
		}
	}
	ix := b.Build()
	if len(ix.Frames) != 1 {
		t.Fatalf("frames=%d want 1", len(ix.Frames))
	}
}

func TestLookupAndSparseHeadMiss(t *testing.T) {
	ix := &Index{Frames: []Keyframe{
		{ID: 0, ByteOffset: 100 * 188, PTS: 0},
		{ID: 1, ByteOffset: 200 * 188, PTS: 90000},
	}}
	if _, ok := ix.Lookup(50 * 188); ok {
		t.Fatal("sparse head must be an explicit seek miss")
	}
	f, ok := ix.Lookup(150 * 188)
	if !ok || f.ID != 0 {
		t.Fatalf("lookup should return nearest prior frame: %+v", f)
	}
	f, ok = ix.Lookup(200 * 188)
	if !ok || f.ID != 1 {
		t.Fatalf("exact lookup failed: %+v", f)
	}
}

func TestVerifySeekReadsSameFrame(t *testing.T) {
	var stream []byte
	raw := ts.BuildPacket(ts.BuildOpts{
		PID: 0x100, PUSI: true, AdaptationControl: 3, RandomAccess: true,
		CC: 2, WithPCR: true, PCRBase: 42, Payload: []byte("keyframe-data")})
	stream = append(stream, make([]byte, 188)...) // one leading packet
	stream = append(stream, raw...)
	b := NewBuilder()
	p, _ := ts.Parse(raw, 188)
	b.Consider(p, 1, 90000)
	ix := b.Build()
	f, ok := ix.Lookup(188)
	if !ok {
		t.Fatal("keyframe missing")
	}
	if err := ix.VerifySeek(f, stream); err != nil {
		t.Fatalf("seek verification: %v", err)
	}
}

func TestVerifySeekTruncatedTail(t *testing.T) {
	b := NewBuilder()
	p := keyframePacket(0x100, true, true, 376)
	b.Consider(p, 2, 0)
	ix := b.Build()
	f, _ := ix.Lookup(376)
	// Stream ends before the indexed packet: truncated tail result, not panic.
	if err := ix.VerifySeek(f, make([]byte, 200)); err == nil {
		t.Fatal("truncated tail must be reported")
	}
}

func TestMP4Roundtrip(t *testing.T) {
	ix := &Index{Frames: []Keyframe{
		{ID: 0, PacketOffset: 1, ByteOffset: 188, PTS: 0},
		{ID: 1, PacketOffset: 9, ByteOffset: 1692, PTS: 90000},
		{ID: 2, PacketOffset: 17, ByteOffset: 3196, PTS: 180000},
	}}
	data, err := ix.SerializeMP4()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if string(data[4:8]) != "ftyp" {
		t.Fatalf("output does not start with an ftyp box: % x", data[:8])
	}
	back, err := DeserializeMP4(data)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if len(back.Frames) != len(ix.Frames) {
		t.Fatalf("frame count %d want %d", len(back.Frames), len(ix.Frames))
	}
	for i := range ix.Frames {
		if back.Frames[i] != ix.Frames[i] {
			t.Fatalf("frame %d changed: %+v vs %+v", i, back.Frames[i], ix.Frames[i])
		}
	}
}

func TestDeserializeRejectsCorruptContainer(t *testing.T) {
	if _, err := DeserializeMP4([]byte{1, 2, 3}); err == nil {
		t.Fatal("garbage input must be rejected")
	}
}
