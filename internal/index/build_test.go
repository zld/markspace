package index

import (
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/fixture"
	mp4 "mediad/internal/mp4"
)

func buildForCamera(t *testing.T, corrupt, camera string, inferSync bool) (*Index, *mp4.Repaired) {
	t.Helper()
	dir := t.TempDir()
	sess, err := fixture.Generate(dir, 42, 8_000_000, corrupt)
	if err != nil {
		t.Fatal(err)
	}
	s, err := mp4.ScanFile(filepath.Join(dir, "media", camera+".mp4"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	specs := map[uint32]mp4.TrackSpec{}
	for _, e := range sess.Manifest.Entries {
		if e.Camera != camera {
			continue
		}
		for _, ts := range e.Tracks {
			specs[ts.TrackID] = mp4.TrackSpec{Timescale: ts.Timescale, OriginNs: e.Recording.StartNs}
		}
	}
	rep, err := mp4.Repair(s, mp4.RepairOptions{TrackSpecs: specs})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	idx, err := Build(rep, Options{InferSync: inferSync})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return idx, rep
}

func TestIndexTimesMatchRepairedTimeline(t *testing.T) {
	idx, rep := buildForCamera(t, fixture.CorruptNone, "cam-a", false)
	ti := idx.Tracks[0]
	if len(ti.Entries) == 0 {
		t.Fatal("no entries")
	}
	if ti.CoveragePct != 100 {
		t.Fatalf("coverage=%.4f", ti.CoveragePct)
	}
	// Every index entry time must equal the repaired NS of that sample.
	byIndex := map[uint32]int64{}
	for _, smp := range rep.Tracks[0].Samples {
		byIndex[smp.Index] = smp.NS
	}
	for _, e := range ti.Entries {
		if got, ok := byIndex[e.Sample]; !ok || got != e.TimeNs {
			t.Fatalf("entry sample %d time=%d repaired=%d", e.Sample, e.TimeNs, got)
		}
		if e.RawOffset <= 0 || e.DataOffset == 0 {
			t.Fatalf("entry %d missing offsets", e.Sample)
		}
	}
	if err := ti.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIndexLookupReturnsPrecedingKeyframe(t *testing.T) {
	idx, _ := buildForCamera(t, fixture.CorruptNone, "cam-a", false)
	ti := idx.Tracks[0]
	// A time 2.5 s in must resolve to the keyframe at or before it.
	target := int64(2_500_000_000)
	e, ok := ti.Lookup(target)
	if !ok {
		t.Fatal("lookup failed")
	}
	if e.TimeNs > target {
		t.Fatalf("lookup returned future keyframe %d > %d", e.TimeNs, target)
	}
	// And it should be within one GOP (1 s) of the target.
	if target-e.TimeNs > int64(clock.NsPerSecond) {
		t.Fatalf("keyframe too far back: %d", target-e.TimeNs)
	}
	// Before the first keyframe there is nothing to seek to.
	if _, ok := ti.Lookup(ti.Entries[0].TimeNs - 1); ok {
		t.Fatal("lookup before start should fail")
	}
}

func TestSparseKeyframesReported(t *testing.T) {
	idx, _ := buildForCamera(t, fixture.CorruptSparseKey, "cam-a", false)
	ti := idx.Tracks[0]
	if !ti.Sparse {
		t.Fatal("expected sparse flag")
	}
	if ti.MaxIntervalNs <= SparseThresholdNs {
		t.Fatalf("max interval %d below threshold", ti.MaxIntervalNs)
	}
	if len(ti.SparseRanges) == 0 {
		t.Fatal("expected sparse ranges")
	}
	sr := ti.SparseRanges[0]
	if sr.FromSample >= sr.ToSample {
		t.Fatalf("bad sparse range: %+v", sr)
	}
	// Coverage and range are still reported so a reviewer sees the trade-off.
	if ti.RangeEndNs <= ti.RangeStartNs {
		t.Fatal("coverage range missing")
	}
}

func TestMissingSyncFlagsFailWithoutInference(t *testing.T) {
	dir := t.TempDir()
	sess, err := fixture.Generate(dir, 42, 8_000_000, fixture.CorruptMissingSync)
	if err != nil {
		t.Fatal(err)
	}
	s, err := mp4.ScanFile(filepath.Join(dir, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := mp4.Repair(s, mp4.RepairOptions{TrackSpecs: map[uint32]mp4.TrackSpec{1: {Timescale: 90000}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(rep, Options{})
	if err == nil {
		t.Fatal("expected index_incomplete without sync flags")
	}
	pe, ok := clock.As(err)
	if !ok || pe.Code != clock.CodeIndexIncomplete {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = sess
}

func TestMissingSyncFlagsInferredAndLabelled(t *testing.T) {
	idx, _ := buildForCamera(t, fixture.CorruptMissingSync, "cam-a", true)
	ti := idx.Tracks[0]
	if !ti.DerivedSync {
		t.Fatal("inferred index must be labelled derived")
	}
	if ti.CoveragePct != 100 {
		t.Fatalf("coverage=%.4f", ti.CoveragePct)
	}
	if len(ti.Entries) < 2 {
		t.Fatalf("expected several derived entries, got %d", len(ti.Entries))
	}
}

func TestSparseReferenceRunwayBeforeFirstKeyframe(t *testing.T) {
	idx, _ := buildForCamera(t, fixture.CorruptNone, "cam-b", false)
	ti := idx.Tracks[0]
	// cam-b has one frame of stored pre-roll (40 ms at 25 fps) trimmed by the
	// edit list. Index times are file-relative, so retained pre-roll appears as
	// negative presentation time rather than being dropped, and coverage stays
	// complete.
	if ti.RangeStartNs != -40_000_000 {
		t.Fatalf("range start=%d", ti.RangeStartNs)
	}
	if ti.OriginNs != 250_000_000 {
		t.Fatalf("origin=%d", ti.OriginNs)
	}
	// Every keyframe reaches every presented sample, including pre-roll.
	if ti.CoveragePct != 100 {
		t.Fatalf("coverage=%.4f", ti.CoveragePct)
	}
}
