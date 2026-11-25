package rtp

import (
	"testing"

	prtp "github.com/pion/rtp"

	"replaycheck/internal/fault"
)

func pkt(seq uint16, ts uint32) *prtp.Packet {
	return &prtp.Packet{
		Header: prtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			PayloadType:    96,
			SSRC:           0x10,
			Marker:         seq%10 == 9,
		},
		Payload: []byte{1, 2, 3, 4},
	}
}

func feed(r *Reorderer, seqs ...uint16) {
	for i, s := range seqs {
		r.Push(pkt(s, uint32(i)*3000))
	}
}

func TestReorderAndDedup(t *testing.T) {
	r := New(Config{Window: 4, ClockRate: 90000})
	feed(r, 4, 3, 2, 1, 2, 5, 6)
	res := r.Finish()
	if res.Duplicate != 1 {
		t.Fatalf("duplicates = %d, want 1", res.Duplicate)
	}
	if res.Received != 7 {
		t.Fatalf("received = %d", res.Received)
	}
	want := []uint16{1, 2, 3, 4, 5, 6}
	if len(res.Packets) != len(want) {
		t.Fatalf("packets = %d, want %d (%v)", len(res.Packets), len(want), res.Summary())
	}
	for i, s := range want {
		if res.Packets[i].Seq != s {
			t.Fatalf("packet[%d] = %d, want %d", i, res.Packets[i].Seq, s)
		}
	}
	if res.Reordered == 0 {
		t.Fatal("expected reorder count > 0")
	}
	if res.Lost != 0 {
		t.Fatalf("unexpected loss %d", res.Lost)
	}
}

func TestBurstLossLocalised(t *testing.T) {
	r := New(Config{Window: 16, ClockRate: 90000})
	feed(r, 1, 2, 3, 8, 9, 10)
	res := r.Finish()
	if res.Lost != 4 {
		t.Fatalf("lost = %d, want 4", res.Lost)
	}
	if len(res.Committed) != 2 {
		t.Fatalf("spans = %d, want 2", len(res.Committed))
	}
	sp := res.Committed[0]
	if sp.GapStart != 4 || sp.GapEnd != 7 {
		t.Fatalf("gap = %d..%d, want 4..7", sp.GapStart, sp.GapEnd)
	}
	iss, ok := res.Issues.First(fault.KindPacketLoss)
	if !ok {
		t.Fatal("missing packet-loss issue")
	}
	if iss.Severity != fault.Damaged {
		t.Fatalf("severity = %v, want damaged", iss.Severity)
	}
}

func TestLatePacketDoesNotRewriteCommit(t *testing.T) {
	r := New(Config{Window: 4, ClockRate: 90000})
	// Advance far enough that positions after 1 and 2 are final, but 3 was
	// never delivered, so a copy of 3 arriving now is late rather than a
	// duplicate.
	feed(r, 1, 2, 30)
	late := pkt(3, 1234)
	r.Push(late)
	if r.result.Late != 1 {
		t.Fatalf("late = %d, want 1", r.result.Late)
	}
	if r.result.Duplicate != 0 {
		t.Fatalf("duplicate = %d, want 0", r.result.Duplicate)
	}
	res := r.Finish()
	for _, p := range res.Packets {
		if p.Seq == 3 {
			t.Fatalf("late packet seq 3 was committed")
		}
	}
	if !res.Issues.HasKind(fault.KindStageStale) {
		t.Fatal("late arrival should be reported as stale")
	}
}

func TestCommittedDuplicateCountsAsDuplicate(t *testing.T) {
	r := New(Config{Window: 4, ClockRate: 90000})
	feed(r, 1, 2, 30)
	// Both 1 and 2 are committed; a second copy of either is a duplicate.
	r.Push(pkt(1, 0))
	r.Push(pkt(2, 3000))
	res := r.Finish()
	if res.Duplicate != 2 {
		t.Fatalf("duplicates = %d, want 2 (%s)", res.Duplicate, res.Summary())
	}
	if res.Late != 0 {
		t.Fatalf("late = %d, want 0", res.Late)
	}
	if len(res.Packets) != 3 {
		t.Fatalf("packets = %d, want 3", len(res.Packets))
	}
}

func TestSequenceWrap(t *testing.T) {
	r := New(Config{Window: 4, ClockRate: 90000})
	feed(r, 65532, 65533, 65534, 65535, 0, 1, 2)
	res := r.Finish()
	if res.Lost != 0 {
		t.Fatalf("lost = %d, want 0 (wrap should be contiguous)", res.Lost)
	}
	if len(res.Packets) != 7 {
		t.Fatalf("packets = %d, want 7", len(res.Packets))
	}
	if res.Packets[4].Seq != 0 {
		t.Fatalf("packet[4] = %d, want 0", res.Packets[4].Seq)
	}
}

func TestTimestampWrapDetected(t *testing.T) {
	r := New(Config{Window: 8, ClockRate: 90000})
	r.Push(pkt(1, 0xFFFFFF00))
	r.Push(pkt(2, 0x00000100))
	res := r.Finish()
	if res.Wraps != 1 {
		t.Fatalf("wraps = %d, want 1", res.Wraps)
	}
	if !res.Issues.HasKind(fault.KindTimestampWrap) {
		t.Fatal("missing timestamp-wrap issue")
	}
}

func TestReorderedTimestampIsNotWrap(t *testing.T) {
	r := New(Config{Window: 8, ClockRate: 90000})
	r.Push(pkt(1, 100000))
	r.Push(pkt(2, 99000)) // small backward step: reorder, not wrap
	res := r.Finish()
	if res.Wraps != 0 {
		t.Fatalf("wraps = %d, want 0", res.Wraps)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	if _, err := Decode([]byte{0x80}); err == nil {
		t.Fatal("expected error")
	}
	p := pkt(7, 1234)
	buf, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.SequenceNumber != 7 || got.Timestamp != 1234 {
		t.Fatalf("decoded %+v", got.Header)
	}
}

func TestEmptyTrace(t *testing.T) {
	res := New(DefaultConfig()).Finish()
	if len(res.Packets) != 0 || res.Lost != 0 {
		t.Fatalf("empty trace produced %+v", res.Summary())
	}
}

func TestBurstLossSpansCarryTimestamps(t *testing.T) {
	r := New(Config{Window: 8, ClockRate: 90000})
	// Timestamps advance 3000 ticks per sequence number.
	for _, s := range []uint16{1, 2, 3, 10, 11, 12} {
		r.Push(pkt(s, uint32(s)*3000))
	}
	res := r.Finish()
	if res.Lost != 6 {
		t.Fatalf("lost = %d, want 6", res.Lost)
	}
	if len(res.Committed) != 2 {
		t.Fatalf("spans = %d, want 2", len(res.Committed))
	}
	sp := res.Committed[0]
	if sp.GapStartTick != 9000 || sp.GapEndTick != 30000 {
		t.Fatalf("gap ticks = %d..%d, want 9000..30000", sp.GapStartTick, sp.GapEndTick)
	}
	if TimestampMillis(sp.GapEndTick, 90000) != 333 {
		t.Fatalf("gap end ms = %d", TimestampMillis(sp.GapEndTick, 90000))
	}
}

func TestWindowLateThenBurst(t *testing.T) {
	// A packet from inside the window may still fill its slot.
	r := New(Config{Window: 16, ClockRate: 90000})
	r.Push(pkt(1, 3000))
	r.Push(pkt(5, 15000))
	r.Push(pkt(2, 6000)) // inside window
	res := r.Finish()
	if res.Lost != 2 {
		t.Fatalf("lost = %d, want 2 (3 and 4)", res.Lost)
	}
	if res.Late != 0 {
		t.Fatalf("late = %d, want 0", res.Late)
	}
	if len(res.Packets) != 3 {
		t.Fatalf("packets = %d, want 3", len(res.Packets))
	}
}
