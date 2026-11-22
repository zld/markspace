package transport

import (
	"testing"
	"time"

	"github.com/pion/rtp"

	"avcheck/internal/media"
)

func mkPacket(t *testing.T, rtpSeq uint16, frameSeq uint64) []byte {
	t.Helper()
	payload := []byte{byte(frameSeq), byte(frameSeq >> 8), 0xAB, 0xCD}
	m := PacketMeta{
		StreamID: 7, FrameSeq: frameSeq, Ticks: int64(frameSeq) * 3000,
		Key: frameSeq%25 == 0, MetaVersion: media.MetaVersion, Length: uint32(len(payload)),
	}
	m.Sum = m.Frame(payload).ComputeSum()
	raw, err := EncodePacket(m, payload, rtpSeq, uint32(frameSeq*3000), 42)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}

func decode(t *testing.T, raw []byte) (*rtp.Packet, PacketMeta) {
	t.Helper()
	p, m, err := DecodePacket(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.MetaVersion != media.MetaVersion {
		t.Fatalf("meta version %d", m.MetaVersion)
	}
	return p, m
}

func TestPacketRoundTrip(t *testing.T) {
	raw := mkPacket(t, 100, 55)
	p, m := decode(t, raw)
	if p.SequenceNumber != 100 || p.SSRC != 42 || p.Marker {
		t.Fatalf("header mismatch: %+v", p.Header)
	}
	if m.StreamID != 7 || m.FrameSeq != 55 || m.Ticks != 165000 || m.Length != 4 {
		t.Fatalf("meta mismatch: %+v", m)
	}
	f := m.Frame(p.Payload)
	if f.ComputeSum() != m.Sum {
		t.Fatal("checksum mismatch on clean packet")
	}
}

func TestPacketTruncated(t *testing.T) {
	raw := mkPacket(t, 1, 1)
	if _, _, err := DecodePacket(raw[:len(raw)/2]); err == nil {
		t.Fatal("expected error on truncated packet")
	}
}

func feed(r *Reorderer, seqs ...uint16) (out []uint16, gaps []GapRecord) {
	for _, s := range seqs {
		raw := mkPacket(&testing.T{}, s, uint64(s))
		p, _, _ := DecodePacket(raw)
		o, g := r.Push(p)
		for _, pkt := range o {
			out = append(out, pkt.SequenceNumber)
		}
		gaps = append(gaps, g...)
	}
	o, g := r.Flush()
	for _, pkt := range o {
		out = append(out, pkt.SequenceNumber)
	}
	return out, append(gaps, g...)
}

func TestReorderDuplicateAndRecover(t *testing.T) {
	r := NewReorderer(1, 8, 3)
	// duplicate of 10, reorder 12/11, then in-order
	out, _ := feed(r, 10, 10, 12, 11, 13, 14)
	want := []uint16{10, 11, 12, 13, 14}
	if len(out) != len(want) {
		t.Fatalf("out=%v want=%v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out=%v want=%v", out, want)
		}
	}
	if r.Stats.Duplicates != 1 || r.Stats.Reordered != 1 {
		t.Fatalf("stats: %+v", r.Stats)
	}
}

func TestReorderWrapAndLate(t *testing.T) {
	r := NewReorderer(1, 8, 3)
	out, _ := feed(r, 65534, 65535, 0, 1, 2)
	want := []uint16{65534, 65535, 0, 1, 2}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("wrap out=%v want=%v", out, want)
		}
	}
	// duplicate of a still-remembered packet is dropped as duplicate
	r2 := NewReorderer(1, 4, 2)
	seqs := make([]uint16, 0, 21)
	for s := uint16(100); s <= 120; s++ {
		seqs = append(seqs, s)
	}
	feed(r2, seqs...)
	if o, _ := r2.Push(mustPkt(t, 120)); len(o) != 0 || r2.Stats.Duplicates != 1 {
		t.Fatalf("duplicate not dropped: %+v", r2.Stats)
	}
	// 100 has fallen out of the dedup ring and behind the window: late
	if o, _ := r2.Push(mustPkt(t, 100)); len(o) != 0 || r2.Stats.Late != 1 {
		t.Fatalf("late packet not dropped: %+v", r2.Stats)
	}
}

func mustPkt(t *testing.T, seq uint16) *rtp.Packet {
	p, _, _ := DecodePacket(mkPacket(t, seq, uint64(seq)))
	return p
}

func TestReorderBurstGap(t *testing.T) {
	r := NewReorderer(1, 4, 3)
	// lose 20,21,22 (burst of 3), then continue
	out, gaps := feed(r, 18, 19, 23, 24, 25, 26, 27)
	for i, s := range []uint16{18, 19, 23, 24, 25, 26, 27} {
		if out[i] != s {
			t.Fatalf("out=%v", out)
		}
	}
	if len(gaps) != 1 || gaps[0].First != 20 || gaps[0].Count != 3 || !gaps[0].Burst {
		t.Fatalf("gaps=%+v", gaps)
	}
	if r.Stats.BurstGaps != 1 || r.Stats.GapPackets != 3 {
		t.Fatalf("stats: %+v", r.Stats)
	}
}

func TestBandwidthShiftAdjustsWindow(t *testing.T) {
	e := NewEstimator(4, 32, 100*time.Millisecond)
	at := time.Unix(0, 0)
	for i := 0; i < 10; i++ {
		at = at.Add(100 * time.Millisecond)
		e.Observe(100000, at) // ~1 MB/s reference
	}
	base := e.WindowHint()
	for i := 0; i < 12; i++ {
		at = at.Add(100 * time.Millisecond)
		e.Observe(10000, at) // bandwidth collapse
	}
	if e.WindowHint() <= base || e.Shifts == 0 {
		t.Fatalf("window did not grow on collapse: hint=%d shifts=%d", e.WindowHint(), e.Shifts)
	}
	for i := 0; i < 30; i++ {
		at = at.Add(100 * time.Millisecond)
		e.Observe(500000, at) // surge
	}
	if e.WindowHint() > 32 || e.WindowHint() < 4 {
		t.Fatalf("hint out of bounds: %d", e.WindowHint())
	}
}
