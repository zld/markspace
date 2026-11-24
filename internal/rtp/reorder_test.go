package rtp

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/fixture"
)

func decodeSession(t *testing.T, plan fixture.RTPPlan) (*Result, fixture.RTPSession) {
	t.Helper()
	sess, err := fixture.BuildRTP(plan)
	if err != nil {
		t.Fatal(err)
	}
	pkts, err := Decode(sess.Wire)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Reorder(pkts)
	if err != nil {
		t.Fatal(err)
	}
	return res, sess
}

func TestReorderDuplicatesCountedOnce(t *testing.T) {
	res, _ := decodeSession(t, fixture.RTPPlan{
		PacketCount: 20, PayloadLen: 16, StartSeq: 100, TSStep: 3000,
		DuplicateAt: []int{3, 4, 4, 10},
	})
	if res.Duplicates != 3 {
		t.Fatalf("duplicates = %d, want 3", res.Duplicates)
	}
	if len(res.Ordered) != 20 {
		t.Fatalf("ordered = %d, want 20 unique", len(res.Ordered))
	}
	if res.Lost != 0 {
		t.Fatalf("lost = %d", res.Lost)
	}
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].SeqExt != res.Ordered[i-1].SeqExt+1 {
			t.Fatalf("sequence not contiguous at %d: %d after %d", i, res.Ordered[i].SeqExt, res.Ordered[i-1].SeqExt)
		}
	}
}

func TestReorderRestoresOrder(t *testing.T) {
	res, _ := decodeSession(t, fixture.RTPPlan{
		PacketCount: 15, PayloadLen: 8, StartSeq: 500, TSStep: 1000,
		ReorderAt: []int{2, 5, 6, 11},
	})
	if res.Reordered == 0 {
		t.Fatal("expected reordered packets to be counted")
	}
	if len(res.Ordered) != 15 || res.Lost != 0 {
		t.Fatalf("ordered=%d lost=%d", len(res.Ordered), res.Lost)
	}
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].SeqExt < res.Ordered[i-1].SeqExt {
			t.Fatalf("delivery out of order at %d", i)
		}
	}
}

func TestReorderBurstLossRetainsRecoverableMarkers(t *testing.T) {
	res, _ := decodeSession(t, fixture.RTPPlan{
		PacketCount: 30, PayloadLen: 8, StartSeq: 10, TSStep: 1000,
		LossStart: 10, LossBurst: 5,
	})
	if res.Lost != 5 {
		t.Fatalf("lost = %d, want 5", res.Lost)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	g := res.Gaps[0]
	if g.Start != 20 || g.End != 24 || g.Count != 5 || !g.Recoverable || g.Reason != "missing" {
		t.Fatalf("gap = %+v", g)
	}
	if len(res.Ordered) != 25 {
		t.Fatalf("delivered = %d", len(res.Ordered))
	}
	// Order must remain strictly increasing across the gap.
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].SeqExt <= res.Ordered[i-1].SeqExt {
			t.Fatalf("duplicate or reversed delivery at %d", i)
		}
	}
}

func TestLatePacketCannotRewriteBoundary(t *testing.T) {
	res, err := Reorder([]Packet{
		{Seq: 1, Timestamp: 100, Payload: []byte{1}},
		{Seq: 2, Timestamp: 200, Payload: []byte{2}},
		{Seq: 3, Timestamp: 300, Payload: []byte{3}},
		{Seq: 99, Timestamp: 9900, Payload: []byte{99}},
		{Seq: 4, Timestamp: 400, Payload: []byte{4}}, // arrives after far-future 99
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Late != 1 {
		t.Fatalf("late = %d, notes=%v", res.Late, res.Notes)
	}
	for _, p := range res.Ordered {
		if p.Seq == 4 {
			t.Fatalf("late packet entered delivered stream: %+v", p)
		}
	}
	foundLateGap := false
	for _, g := range res.Gaps {
		if g.Start == 4 && g.Reason == "late_dropped" {
			foundLateGap = true
		}
	}
	if !foundLateGap {
		t.Fatalf("late packet not marked in gaps: %+v", res.Gaps)
	}
	// Sequence numbers before 4 stay confirmed.
	if res.Ordered[0].Seq != 1 || res.Ordered[len(res.Ordered)-1].Seq != 99 {
		t.Fatalf("boundary rewritten: %+v", res.Ordered)
	}
}

func TestTimestampWraparoundExpanded(t *testing.T) {
	res, _ := decodeSession(t, fixture.RTPPlan{
		PacketCount: 12, PayloadLen: 4, StartSeq: 1, TSStep: 400, TSWrap: true,
	})
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].TSExt <= res.Ordered[i-1].TSExt {
			t.Fatalf("timestamp not monotonic after wrap at %d: %d -> %d", i, res.Ordered[i-1].TSExt, res.Ordered[i].TSExt)
		}
	}
}

func TestSequenceWraparoundExpanded(t *testing.T) {
	res, _ := decodeSession(t, fixture.RTPPlan{
		PacketCount: 8, PayloadLen: 4, StartSeq: 65532, TSStep: 100,
	})
	if res.Duplicates != 0 || res.Lost != 0 || len(res.Ordered) != 8 {
		t.Fatalf("dups=%d lost=%d ordered=%d", res.Duplicates, res.Lost, len(res.Ordered))
	}
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].SeqExt != res.Ordered[i-1].SeqExt+1 {
			t.Fatalf("wraparound broke contiguity: %d -> %d", res.Ordered[i-1].SeqExt, res.Ordered[i].SeqExt)
		}
	}
}

func TestDecodeRejectsMalformedAndOversized(t *testing.T) {
	if _, err := Decode([][]byte{{0x80}}); clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("malformed kind = %q", clock.AsKind(err))
	}
	big := make([]byte, 12+MaxPayload+1)
	big[0] = 0x80
	if _, err := Decode([][]byte{big}); clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("oversized kind = %q", clock.AsKind(err))
	}
}
