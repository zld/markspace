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
	// Advance far enough that 1 and 2 are committed and positions after them
	// are final.
	feed(r, 1, 2, 30)
	late := pkt(2, 1234)
	r.Push(late)
	if r.result.Late != 1 {
		t.Fatalf("late = %d, want 1", r.result.Late)
	}
	res := r.Finish()
	n := 0
	for _, p := range res.Packets {
		if p.Seq == 2 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("seq 2 appears %d times, want exactly the original copy", n)
	}
	if !res.Issues.HasKind(fault.KindStageStale) {
		t.Fatal("late arrival should be reported as stale")
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
