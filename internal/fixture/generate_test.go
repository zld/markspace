package fixture

import (
	"strings"
	"testing"

	"mediakernel/internal/manifest"
	"mediakernel/internal/rtp"
)

func TestGeneratorDeterministic(t *testing.T) {
	g1 := NewGenerator(42).Build(8, []int{900, 2500, 700})
	g2 := NewGenerator(42).Build(8, []int{900, 2500, 700})
	if g1.DASHMPD != g2.DASHMPD || g1.HLSPlaylist != g2.HLSPlaylist {
		t.Fatal("manifest output not deterministic")
	}
	if len(g1.Cases) != len(g2.Cases) {
		t.Fatalf("case count = %d vs %d", len(g1.Cases), len(g2.Cases))
	}
	for i := range g1.Cases {
		a, b := g1.Cases[i], g2.Cases[i]
		if a.Name != b.Name || len(a.Packets) != len(b.Packets) {
			t.Fatalf("case %s diverged", a.Name)
		}
		for j := range a.Packets {
			if a.Packets[j].SequenceNumber != b.Packets[j].SequenceNumber ||
				a.Packets[j].Timestamp != b.Packets[j].Timestamp {
				t.Fatalf("case %s packet %d diverged", a.Name, j)
			}
		}
	}
	// Different seed must change payload bytes (but keep the geometry).
	g3 := NewGenerator(43).Build(8, nil)
	if g3.Cases[0].Packets[0].Payload[20] == g1.Cases[0].Packets[0].Payload[20] {
		t.Fatal("different seeds produced identical random payload")
	}
}

func TestAllFaultCasesPresent(t *testing.T) {
	f := NewGenerator(42).Build(8, nil)
	want := map[string]bool{
		CaseNormal: false, CaseDuplicate: false, CaseReorder: false,
		CaseBurstLoss: false, CaseBandwidth: false, CaseTSWrap: false,
	}
	for _, c := range f.Cases {
		if _, ok := want[c.Name]; !ok {
			t.Fatalf("unexpected case %s", c.Name)
		}
		want[c.Name] = true
		if len(c.Packets) == 0 {
			t.Fatalf("case %s has no packets", c.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing fault case %s", name)
		}
	}
}

func TestDuplicateCaseReordersAndDedups(t *testing.T) {
	f := NewGenerator(42).Build(8, nil)
	var dup FaultCase
	for _, c := range f.Cases {
		if c.Name == CaseDuplicate {
			dup = c
		}
	}
	if len(dup.DuplicateSeqs) == 0 {
		t.Fatal("expected duplicate sequence metadata")
	}
	rr, err := rtp.NewReorderer(rtp.Config{Window: 16})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range dup.Packets {
		rr.Push(dup.StreamID, p)
	}
	_, stats, _ := rr.Flush(dup.StreamID)
	if stats.Duplicates != len(dup.DuplicateSeqs) {
		t.Fatalf("dedup = %d, want %d", stats.Duplicates, len(dup.DuplicateSeqs))
	}
	if stats.RecoveryRate() != 1 {
		t.Fatalf("duplicates must not reduce recovery: %f", stats.RecoveryRate())
	}
}

func TestBurstLossCaseReportsLoss(t *testing.T) {
	f := NewGenerator(42).Build(8, nil)
	var burst FaultCase
	for _, c := range f.Cases {
		if c.Name == CaseBurstLoss {
			burst = c
		}
	}
	if burst.LossBurst != 8 || len(burst.LostSeqs) != 8 {
		t.Fatalf("burst metadata = %+v", burst)
	}
	rr, _ := rtp.NewReorderer(rtp.Config{Window: 4})
	var pushErrs int
	for _, p := range burst.Packets {
		_, errs := rr.Push(burst.StreamID, p)
		pushErrs += len(errs)
	}
	_, stats, flushErrs := rr.Flush(burst.StreamID)
	if stats.UnrecoveredPackets < 8 {
		t.Fatalf("unrecovered = %d, want >= 8", stats.UnrecoveredPackets)
	}
	if pushErrs+len(flushErrs) == 0 {
		t.Fatal("burst loss must produce structured errors")
	}
}

func TestWrapCaseCrossesTimestampBoundary(t *testing.T) {
	pkts := NewGenerator(42).WrapPackets()
	rr, _ := rtp.NewReorderer(rtp.Config{Window: 4})
	var maxTS uint64
	for _, p := range pkts {
		out, _ := rr.Push("video", p)
		for _, o := range out {
			if o.MonotonicTS > maxTS {
				maxTS = o.MonotonicTS
			}
		}
	}
	if maxTS < 0x100000000 {
		t.Fatalf("unwrapped timestamp did not cross 2^32, max = %d", maxTS)
	}
}

func TestGeneratedDASHParses(t *testing.T) {
	f := NewGenerator(42).Build(8, nil)
	doc, err := manifest.ParseDASH(strings.NewReader(f.DASHMPD))
	if err != nil {
		t.Fatalf("generated DASH must parse: %v", err)
	}
	if len(doc.Representations) != 3 {
		t.Fatalf("reps = %d, want 3", len(doc.Representations))
	}
	if doc.Representations[0].Segments[1].StartTick != 90000 {
		t.Fatal("segment grid not on the master clock")
	}
}

func TestDamagedDASHFailsAtParseStage(t *testing.T) {
	g := NewGenerator(42)
	if _, err := manifest.ParseDASH(strings.NewReader(g.DamagedDASH())); err == nil {
		t.Fatal("zero-timescale fixture must fail parsing")
	}
}

func TestDamagedHLSIsolatesRegion(t *testing.T) {
	f := NewGenerator(42).Build(8, nil)
	doc, err := manifest.ParseHLS(strings.NewReader(f.HLSDamaged))
	if err != nil {
		t.Fatalf("damage is reported, not fatal: %v", err)
	}
	if len(doc.Errors) == 0 || len(doc.Isolated) == 0 {
		t.Fatal("damaged HLS must report and isolate the broken region")
	}
	if len(doc.Groups) < 2 {
		t.Fatal("discontinuity must split the playlist into groups")
	}
}
