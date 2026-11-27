package timeline

import (
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

var spec = ingest.TrackSpec{ID: "a", Codec: ingest.CodecOpus, ClockRate: 48000}

// ingested builds a packet through the real RTP ingest path so its extended
// sequence and duration are exactly what production code would produce.
func ingested(t *testing.T, r *Reorderer, seq uint16, ts uint32) *ingest.AudioPacket {
	t.Helper()
	wire := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 1,
		},
		// Legal 20 ms Opus code-0 payload: TOC config 0 + one frame byte.
		Payload: []byte{0x00, 0xAA},
	}
	raw, err := wire.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := ingest.ParseRTP(spec, raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ingest.Parse(spec, pkt); err != nil {
		t.Fatal(err)
	}
	return pkt
}

func seqNums(pkts []*ingest.AudioPacket) []uint64 {
	out := make([]uint64, len(pkts))
	for i, p := range pkts {
		out[i] = p.ExtSequence
	}
	return out
}

func TestReorderOutOfOrderAndDuplicate(t *testing.T) {
	r := NewReorderer("a", 8)
	// Feed 0,2,1: 0 flushes alone, 2 buffers, then 1 releases 1 and 2.
	if res := r.Push(ingested(t, r, 0, 0)); len(res.Flushed) != 1 {
		t.Fatalf("first push flushed %v", seqNums(res.Flushed))
	}
	if res := r.Push(ingested(t, r, 2, 2)); len(res.Flushed) != 0 {
		t.Fatal("gap packet must stay buffered")
	}
	res := r.Push(ingested(t, r, 1, 1))
	if len(res.Flushed) != 2 || seqNums(res.Flushed)[0] != 1 || seqNums(res.Flushed)[1] != 2 {
		t.Fatalf("want flush 1,2 got %v", seqNums(res.Flushed))
	}
	dup := r.Push(ingested(t, r, 1, 1))
	if !dup.Duplicate || r.Duplicates != 1 || r.Accepted != 3 {
		t.Fatalf("duplicate handling: %+v counters=%d/%d", dup, r.Duplicates, r.Accepted)
	}
}

func TestReorderGapRecordedNotFilled(t *testing.T) {
	r := NewReorderer("a", 8)
	r.Push(ingested(t, r, 0, 0))
	// seq 3 arrives while 1 and 2 are missing: it stays buffered and no audio
	// is fabricated for the hole.
	if res := r.Push(ingested(t, r, 3, 3)); len(res.Flushed) != 0 {
		t.Fatalf("seq 3 must stay buffered, got %v", seqNums(res.Flushed))
	}
	rest, endGaps := r.Flush()
	if len(rest) != 1 || rest[0].Sequence != 3 {
		t.Fatalf("flush = %v", seqNums(rest))
	}
	if len(endGaps) != 1 || endGaps[0].FromSeq != 1 || endGaps[0].ToSeq != 2 {
		t.Fatalf("want one gap 1..2, got %+v", endGaps)
	}
	if len(r.Gaps) != 1 {
		t.Fatalf("gap recorded %d times, want 1: %+v", len(r.Gaps), r.Gaps)
	}
}

func TestLatePacketOutsideWindow(t *testing.T) {
	r := NewReorderer("a", 4)
	for _, s := range []uint16{0, 1, 2, 3, 4, 10} {
		r.Push(ingested(t, r, s, uint32(s)))
	}
	// Seq 5 is 5 behind high-water mark 10, window is 4 -> late.
	res := r.Push(ingested(t, r, 5, 5))
	if res.Late == nil || model.AsError(res.Late).Code != model.ErrSeqLate {
		t.Fatalf("want ERR_SEQ_LATE, got %+v", res.Late)
	}
}

func TestRolloverReorder(t *testing.T) {
	r := NewReorderer("a", 16)
	first := r.Push(ingested(t, r, 0xFFFE, 0xFFFE))
	if len(first.Flushed) != 1 || first.Flushed[0].ExtSequence != 0xFFFE {
		t.Fatalf("first packet must flush: %v", seqNums(first.Flushed))
	}
	second := r.Push(ingested(t, r, 0xFFFF, 0xFFFF))
	if len(second.Flushed) != 1 {
		t.Fatalf("second packet must flush: %v", seqNums(second.Flushed))
	}
	res := r.Push(ingested(t, r, 0, 0))
	if len(res.Flushed) != 1 || res.Flushed[0].ExtSequence != 0x10000 {
		t.Fatalf("rollover flush = %v", seqNums(res.Flushed))
	}
}

func TestBuildTimelineNoFabrication(t *testing.T) {
	r := NewReorderer("a", 8)
	p0 := ingested(t, r, 0, 0)
	r.Push(p0)
	p3 := ingested(t, r, 3, 3)
	r.Push(p3)
	pkts := []*ingest.AudioPacket{p0, p3}
	tl, err := BuildTimeline(spec, pkts, []Gap{{FromSeq: 1, ToSeq: 2}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Frames) != 2 {
		t.Fatalf("frames = %d, want 2 (no fabricated gap audio)", len(tl.Frames))
	}
	if tl.TotalDurNS != 40*model.Millisecond {
		t.Fatalf("total duration must exclude gap: %d", tl.TotalDurNS)
	}
	if tl.Frames[1].StartNS != 20*model.Millisecond {
		t.Fatalf("second frame start = %d", tl.Frames[1].StartNS)
	}
}
