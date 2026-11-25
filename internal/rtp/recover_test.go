package rtp

import (
	"testing"

	"mediasync/internal/model"
)

// streamFixture builds an ordered stream with sequence numbers 0..9 minus the
// supplied missing set, plus redundancy entries for some missing numbers.
func streamFixture(missing, redundant map[uint16]bool) ([]OrderedPacket, map[uint16][]byte) {
	var pkts []OrderedPacket
	for seq := uint16(0); seq < 10; seq++ {
		if missing[seq] {
			continue
		}
		pkts = append(pkts, OrderedPacket{Sequence: seq, Payload: []byte{byte(seq)}})
	}
	red := map[uint16][]byte{}
	for seq := range redundant {
		red[seq] = []byte{0xF0 + byte(seq)}
	}
	return pkts, red
}

func TestRecoverGapsInsideWindow(t *testing.T) {
	missing := map[uint16]bool{2: true, 5: true}
	pkts, red := streamFixture(missing, map[uint16]bool{2: true, 5: true})
	rec := NewRecoverer("video-hi", 8, red)
	out := rec.AnalyzeWindow(pkts, func(seq uint16) bool { return true })

	if len(out) != 10 {
		t.Fatalf("repaired stream length = %d, want 10", len(out))
	}
	for i, p := range out {
		if p.Sequence != uint16(i) {
			t.Fatalf("position %d gap not filled: %+v", i, p)
		}
	}
	if out[2].Payload[0] != 0xF2 {
		t.Fatalf("repaired payload must come from redundancy, got %x", out[2].Payload)
	}
	st := rec.Stats()
	if st.Gaps != 2 || st.Recovered != 2 || st.Lost != 0 {
		t.Fatalf("recovery counters wrong: %+v", st)
	}
}

func TestUnrecoverableGapIsLocatedNotSynthesized(t *testing.T) {
	missing := map[uint16]bool{3: true}
	pkts, red := streamFixture(missing, map[uint16]bool{}) // no redundancy for seq 3
	rec := NewRecoverer("dialogue-core", 8, red)
	out := rec.AnalyzeWindow(pkts, func(seq uint16) bool { return true })

	if len(out) != 9 {
		t.Fatalf("missing packet must not be fabricated: got %d packets", len(out))
	}
	for _, p := range out {
		if p.Sequence == 3 {
			t.Fatal("seq 3 must be absent, never invented")
		}
	}
	st := rec.Stats()
	if st.Gaps != 1 || st.Lost != 1 || st.Recovered != 0 {
		t.Fatalf("counters wrong: %+v", st)
	}
	losses := rec.Losses()
	if len(losses) != 1 || model.Locate(losses[0]) != 3 {
		t.Fatalf("loss must preserve position 3: %+v", losses)
	}
}

func TestBoundaryFailureBlocksRecovery(t *testing.T) {
	missing := map[uint16]bool{4: true}
	pkts, red := streamFixture(missing, map[uint16]bool{4: true})
	rec := NewRecoverer("video", 8, red)
	out := rec.AnalyzeWindow(pkts, func(seq uint16) bool { return seq != 4 })

	if len(out) != 9 {
		t.Fatalf("boundary refusal must keep the gap, got %d packets", len(out))
	}
	st := rec.Stats()
	if st.Recovered != 0 || st.Lost != 1 {
		t.Fatalf("boundary-gated recovery wrong: %+v", st)
	}
	if model.Classify(rec.Losses()[0]) != model.KindMissingKeyframe {
		t.Fatalf("want %s, got %v", model.KindMissingKeyframe, rec.Losses()[0])
	}
}

func TestRecoveryWindowAndDeterminism(t *testing.T) {
	// Gap at seq 1 with high=40 is beyond window=4, so repair is never tried.
	pkts := []OrderedPacket{{Sequence: 0}, {Sequence: 38}, {Sequence: 39}, {Sequence: 40}}
	red := map[uint16][]byte{1: {0xAA}}
	rec := NewRecoverer("far", 4, red)
	out := rec.AnalyzeWindow(pkts, func(seq uint16) bool { return true })
	if len(out) != 4 {
		t.Fatalf("out-of-window gap must not be filled, got %d", len(out))
	}
	st := rec.Stats()
	if st.WindowSkips != 37 || st.Lost != 37 || st.Recovered != 0 {
		// sequences 1..37 are all beyond the window relative to high=40
		t.Fatalf("window accounting wrong: %+v", st)
	}

	// Re-running an identical window must produce identical output and losses.
	missing := map[uint16]bool{2: true, 6: true}
	fixture, redundancy := streamFixture(missing, map[uint16]bool{2: true})
	r1 := NewRecoverer("det", 8, redundancy)
	r2 := NewRecoverer("det", 8, redundancy)
	o1 := r1.AnalyzeWindow(fixture, nil)
	o2 := r2.AnalyzeWindow(fixture, nil)
	if len(o1) != len(o2) {
		t.Fatal("output length differs across runs")
	}
	for i := range o1 {
		if o1[i].Sequence != o2[i].Sequence || string(o1[i].Payload) != string(o2[i].Payload) {
			t.Fatalf("packet %d differs across runs", i)
		}
	}
	if r1.Stats() != r2.Stats() || len(r1.Losses()) != len(r2.Losses()) {
		t.Fatal("recovery decisions must be deterministic")
	}
}
