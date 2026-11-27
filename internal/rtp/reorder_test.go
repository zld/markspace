package rtp_test

import (
	"testing"

	"github.com/pion/rtp"

	"media-check/internal/fault"
	rtpbuf "media-check/internal/rtp"
)

func pkt(seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SSRC: 1, SequenceNumber: seq,
			Timestamp: uint32(seq) * 3000,
		},
		Payload: []byte{1, 2, 3, 4},
	}
}

func collect(buf *rtpbuf.ReorderBuffer, p *rtp.Packet) []uint64 {
	out := buf.Push(p)
	seqs := make([]uint64, 0, len(out))
	for _, op := range out {
		seqs = append(seqs, op.ExtSeq)
	}
	return seqs
}

func flushSeq(buf *rtpbuf.ReorderBuffer) []uint64 {
	out := buf.Flush()
	seqs := make([]uint64, 0, len(out))
	for _, op := range out {
		seqs = append(seqs, op.ExtSeq)
	}
	return seqs
}

func TestInOrderAndDuplicate(t *testing.T) {
	fm := fault.NewModel()
	buf := rtpbuf.NewReorderBuffer(1, 1, rtpbuf.DefaultWindowSize, fm)

	var got []uint64
	for seq := uint16(0); seq < 10; seq++ {
		got = append(got, collect(buf, pkt(seq))...)
	}
	// Re-send seq 5: must be counted once and never re-emitted.
	if dups := collect(buf, pkt(5)); len(dups) != 0 {
		t.Fatalf("duplicate packet re-emitted: %v", dups)
	}
	got = append(got, flushSeq(buf)...)

	want := []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if len(got) != len(want) {
		t.Fatalf("emitted %d packets, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %d want %d", i, got[i], want[i])
		}
	}
	dupes, gaps := buf.Stats()
	if dupes != 1 {
		t.Fatalf("duplicates = %d, want 1", dupes)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected loss gaps: %v", gaps)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	fm := fault.NewModel()
	buf := rtpbuf.NewReorderBuffer(1, 1, rtpbuf.DefaultWindowSize, fm)

	// 0,1,2 arrive, then 4,3: 3 must be emitted before 4 in output order.
	collect(buf, pkt(0))
	collect(buf, pkt(1))
	collect(buf, pkt(2))
	if out := collect(buf, pkt(4)); len(out) != 0 {
		t.Fatalf("packet 4 emitted before 3 arrived: %v", out)
	}
	out := collect(buf, pkt(3))
	got := append(out, flushSeq(buf)...)
	want := []uint64{3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reorder failed at %d: got %d want %d", i, got[i], want[i])
		}
	}
	if _, gaps := buf.Stats(); len(gaps) != 0 {
		t.Fatalf("reorder inside window must not be a loss, got %v", gaps)
	}
}

func TestGapRecordedAsLoss(t *testing.T) {
	fm := fault.NewModel()
	buf := rtpbuf.NewReorderBuffer(1, 1, 8, fm)

	// Deliver 0,1 then skip 2..4. The stream stays inside the window while
	// packets 5..9 arrive, so the hole is finalized once, as a single gap,
	// by Flush — no media is fabricated for the absent packets.
	collect(buf, pkt(0))
	collect(buf, pkt(1))
	for seq := uint16(5); seq < 10; seq++ {
		collect(buf, pkt(seq))
	}
	flushed := buf.Flush()
	if len(flushed) == 0 {
		t.Fatal("expected buffered packets to flush")
	}
	dupes, gaps := buf.Stats()
	if dupes != 0 {
		t.Fatalf("unexpected duplicates: %d", dupes)
	}
	if len(gaps) != 1 || gaps[0].Start != 2 || gaps[0].End != 4 {
		t.Fatalf("loss gap = %v, want single gap [2,4]", gaps)
	}
	losses := fm.ByTrack(1)
	found := false
	for _, f := range losses {
		if f.Code == fault.CodeLost && f.SeqStart == 2 && f.SeqEnd == 4 {
			found = true
		}
	}
	if !found {
		t.Fatalf("loss fault missing from model: %+v", losses)
	}
}

func TestSequenceWrapUnrolled(t *testing.T) {
	fm := fault.NewModel()
	buf := rtpbuf.NewReorderBuffer(1, 1, rtpbuf.DefaultWindowSize, fm)

	// Walk across the 16-bit boundary: 65533, 65534, 65535, 0, 1, 2.
	var emitted []uint64
	for _, seq := range []uint16{65533, 65534, 65535, 0, 1, 2} {
		emitted = append(emitted, collect(buf, pkt(seq))...)
	}
	emitted = append(emitted, flushSeq(buf)...)

	want := []uint64{65533, 65534, 65535, 65536, 65537, 65538}
	if len(emitted) != len(want) {
		t.Fatalf("wrap output = %v, want %v", emitted, want)
	}
	for i := range want {
		if emitted[i] != want[i] {
			t.Fatalf("wrap at %d: got %d want %d", i, emitted[i], want[i])
		}
	}
	if _, gaps := buf.Stats(); len(gaps) != 0 {
		t.Fatalf("clean wrap must not create loss gaps: %v", gaps)
	}
}

func TestLateDuplicateAfterGapFinalized(t *testing.T) {
	fm := fault.NewModel()
	buf := rtpbuf.NewReorderBuffer(1, 1, 4, fm)

	collect(buf, pkt(0))
	// Skip 1, push through the window so seq 1 is finalized as lost.
	for seq := uint16(2); seq < 8; seq++ {
		collect(buf, pkt(seq))
	}
	_, gaps := buf.Stats()
	if len(gaps) != 1 {
		t.Fatalf("expected one finalized gap, got %v", gaps)
	}
	// Late retransmission of 1: must not resurrect or duplicate media.
	if out := collect(buf, pkt(1)); len(out) != 0 {
		t.Fatalf("late packet emitted after finalization: %v", out)
	}
	dupes, _ := buf.Stats()
	if dupes != 1 {
		t.Fatalf("late packet duplicate count = %d, want 1", dupes)
	}
}
