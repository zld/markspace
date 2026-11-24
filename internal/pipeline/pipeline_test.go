package pipeline

import (
	"testing"

	"mediamerge/internal/manifest"
	"mediamerge/internal/model"
)

func runFixtures(t *testing.T) *Suite {
	t.Helper()
	return Run(Fixtures(17, 2000), Options{Seed: 17, SegmentMs: 2000, TargetBps: 640000})
}

func TestSuiteExitCodes(t *testing.T) {
	suite := runFixtures(t)
	want := map[string]int{"normal": ExitOK, "damaged": ExitDamaged, "recovered": ExitOK}
	for _, p := range suite.Paths {
		if got := want[p.Name]; p.ExitCode != got {
			t.Fatalf("%s exit=%d want %d", p.Name, p.ExitCode, got)
		}
	}
	if len(suite.Paths) != 3 {
		t.Fatalf("paths=%d", len(suite.Paths))
	}
}

func TestSuiteAcceptanceTargets(t *testing.T) {
	doc := runFixtures(t).Doc
	if doc.Segments < 4 || doc.Segments > 20 {
		t.Fatalf("segments=%d", doc.Segments)
	}
	if doc.SilenceMs < 0 || doc.SilenceMs > 8000 {
		t.Fatalf("silence=%d", doc.SilenceMs)
	}
	if doc.Duplicates < 0 || doc.Duplicates > 5 {
		t.Fatalf("duplicates=%d", doc.Duplicates)
	}
	if doc.TargetBitrateBps != 640000 {
		t.Fatalf("target=%d", doc.TargetBitrateBps)
	}
	if dev := doc.BitrateDeviationPct; dev > 10 || dev < -10 {
		t.Fatalf("bitrate deviation %.2f%%", dev)
	}
	if doc.QualitySamples <= 0 {
		t.Fatal("no quality samples")
	}
	if doc.AlignmentErrorMs > 20 {
		t.Fatalf("alignment=%.2fms", doc.AlignmentErrorMs)
	}
	if len(doc.RecoveredTracks) < 1 || len(doc.IsolatedTracks) < 1 {
		t.Fatalf("recovered=%v isolated=%v", doc.RecoveredTracks, doc.IsolatedTracks)
	}
	if len(doc.ManifestSHA256) != 64 {
		t.Fatalf("hash=%q", doc.ManifestSHA256)
	}
}

func TestDamagedPathPropagation(t *testing.T) {
	suite := runFixtures(t)
	var damaged manifest.PathRecord
	for _, p := range suite.Paths {
		if p.Name == "damaged" {
			damaged = p
		}
	}
	// mfhd gap survives to the manifest.
	if len(damaged.Gaps) != 1 || damaged.Gaps[0].BeforeSeq != 5 {
		t.Fatalf("gaps=%+v", damaged.Gaps)
	}
	// Truncation error must be locatable.
	locatable := false
	for _, e := range damaged.Diagnostics {
		if e.Op == "ingest" && e.Level == model.LevelIsolated && e.ByteOffset >= 0 {
			locatable = true
		}
	}
	if !locatable {
		t.Fatal("truncated box diagnostic lacks a byte offset")
	}
	// The healthy video track keeps producing segments even though two
	// audio tracks were isolated.
	if len(damaged.Segments) == 0 {
		t.Fatal("unrelated track stopped processing")
	}
	for _, seg := range damaged.Segments {
		for _, id := range seg.Tracks {
			if id != 1 {
				t.Fatalf("isolated track leaked into segments: %+v", seg)
			}
		}
	}
	// Isolated tracks must never be reported as mixable.
	for _, tr := range damaged.Tracks {
		for _, id := range damaged.IsolatedTracks {
			if tr.ID == id && tr.Mixable {
				t.Fatalf("isolated track %d still mixable", id)
			}
		}
	}
}

func TestRecoveredPathRepairsRollback(t *testing.T) {
	suite := runFixtures(t)
	for _, p := range suite.Paths {
		if p.Name != "recovered" {
			continue
		}
		if len(p.RecoveredTracks) != 1 || p.RecoveredTracks[0] != 2 {
			t.Fatalf("recovered=%v", p.RecoveredTracks)
		}
		if len(p.IsolatedTracks) != 0 {
			t.Fatalf("isolated=%v", p.IsolatedTracks)
		}
		// Every segment must carry both tracks once the rollback is fixed.
		for _, seg := range p.Segments {
			if len(seg.Tracks) != 2 {
				t.Fatalf("segment %d tracks=%v", seg.Index, seg.Tracks)
			}
		}
		return
	}
	t.Fatal("recovered path missing")
}

func TestSuiteDeterministic(t *testing.T) {
	a := runFixtures(t)
	b := runFixtures(t)
	ha, err := a.Doc.Hash()
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := b.Doc.Hash()
	if ha != hb {
		t.Fatalf("manifest hash differs: %s vs %s", ha, hb)
	}
	if a.Doc.Segments != b.Doc.Segments || a.Doc.Duplicates != b.Doc.Duplicates {
		t.Fatal("suite decisions differ between runs")
	}
}

func TestByteCapRejectsOversizedInput(t *testing.T) {
	// Five 14 MiB files exceed the 64 MiB total input limit.
	files := map[string][]byte{}
	for i := 0; i < 5; i++ {
		files[string(rune('a'+i))+".mp4"] = make([]byte, 14<<20)
	}
	fixtures := []Fixture{{Name: "oversized", Expect: ExitBadInput, Files: files}}
	suite := Run(fixtures, Options{Seed: 1, SegmentMs: 2000, TargetBps: 640000})
	if suite.Paths[0].ExitCode != ExitBadInput {
		t.Fatalf("exit=%d", suite.Paths[0].ExitCode)
	}
	found := false
	for _, e := range suite.Paths[0].Diagnostics {
		if e.Level == model.LevelFatal && e.Op == "ingest" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing 64 MiB limit diagnostic")
	}
}
