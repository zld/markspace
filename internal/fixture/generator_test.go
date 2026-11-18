package fixture

import (
	"testing"

	"rtspdiag/internal/rtp"
)

func TestGenerateDeterministic(t *testing.T) {
	p := DefaultPlan(5)
	a := Generate(42, p)
	b := Generate(42, p)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if string(a[i].Raw) != string(b[i].Raw) || a[i].Seq != b[i].Seq {
			t.Fatalf("item %d not deterministic", i)
		}
	}
	if c := Generate(7, p); string(c[0].Raw) == string(a[0].Raw) {
		t.Fatalf("different seeds produced identical bytes")
	}
}

func TestGenerateFaults(t *testing.T) {
	plan := DefaultPlan(5)
	items := Generate(42, plan)

	rtpCount, malformed := 0, 0
	seqs := make(map[uint16]int)
	var order []uint16
	for _, it := range items {
		switch it.Kind {
		case KindRTP:
			rtpCount++
			seqs[it.Seq]++
			order = append(order, it.Seq)
		case KindMalformed:
			malformed++
		}
	}
	if malformed != 1 {
		t.Fatalf("expected 1 malformed item, got %d", malformed)
	}
	// total RTP = generated packets - 1 missing + 1 duplicate.
	wantRTP := plan.Frames*PacketsPerFrame - 1 + 1
	if rtpCount != wantRTP {
		t.Fatalf("RTP item count: got %d want %d", rtpCount, wantRTP)
	}
	if _, ok := seqs[plan.MissingSeq]; ok {
		t.Fatalf("missing sequence %d was emitted", plan.MissingSeq)
	}
	if seqs[plan.DuplicateSeq] != 2 {
		t.Fatalf("duplicate sequence count wrong: %d", seqs[plan.DuplicateSeq])
	}
	// The swapped pair must appear in reverse order at adjacent delivery
	// positions. Track every occurrence rather than just the first.
	var first, second int = -1, -1
	for i, s := range order {
		switch s {
		case plan.SwapAdjacent[0]:
			if first == -1 {
				first = i
			}
		case plan.SwapAdjacent[1]:
			if second == -1 {
				second = i
			}
		}
	}
	if first == -1 || second == -1 {
		t.Fatalf("swap pair missing from delivery order")
	}
	if second != first-1 {
		t.Fatalf("swap not applied: %v landed at positions %d and %d",
			plan.SwapAdjacent, first, second)
	}
}

func TestGenerateWraparoundAndParsable(t *testing.T) {
	plan := DefaultPlan(5)
	items := Generate(42, plan)
	parser := rtp.NewParser(1500)

	sawWrapped := false
	parseErrors := 0
	frames := 0
	for _, it := range items {
		pkt, err := parser.Parse(it.Raw)
		if err != nil {
			parseErrors++
			continue
		}
		if pkt.Header.SSRC != SSRC {
			t.Fatalf("unexpected SSRC: %x", pkt.Header.SSRC)
		}
		if pkt.Header.SequenceNumber < plan.WrapAt {
			sawWrapped = true
		}
		if pkt.Header.Marker {
			frames++
		}
	}
	if !sawWrapped {
		t.Fatalf("stream never crossed the uint16 wrap boundary")
	}
	if parseErrors != 1 {
		t.Fatalf("expected exactly 1 parse error, got %d", parseErrors)
	}
	if frames != plan.Frames {
		t.Fatalf("expected %d marker frames, got %d", plan.Frames, frames)
	}
}
