package transform_test

import (
	"testing"

	"mediacore/internal/fault"
	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
	"mediacore/internal/timeline"
	"mediacore/internal/transform"
)

func buildTracks(t *testing.T, kinds ...fixture.FaultKind) ([]*timeline.Track, []ingest.Fragment, *fixture.InjectedStream) {
	t.Helper()
	injected, err := fixture.Inject(41, 2, 24, kinds)
	if err != nil {
		t.Fatal(err)
	}
	r, f := ingest.NewReader("s.fmp4", injected.Data, ingest.DefaultLimits())
	if f != nil {
		t.Fatal(f)
	}
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatal(f)
	}
	idx, _ := ingest.ScanIndex(r, info.MoovOffset+info.MoovSize)
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, idx)
	tracks, _ := timeline.BuildTracks([]*ingest.InitInfo{info}, res.Fragments, 90000)
	return tracks, res.Fragments, injected
}

func TestRepairDropsDuplicateFragment(t *testing.T) {
	tracks, _, _ := buildTracks(t, fixture.FaultDuplicate)
	report, repaired := transform.Repair(tracks, transform.RepairOptions{})
	if report.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", report.Duplicates)
	}
	if len(report.Discontinuities) != 1 || report.Discontinuities[0].Kind != "duplicate" {
		t.Fatalf("discontinuities=%+v", report.Discontinuities)
	}
	dropped := report.Discontinuities[0]
	if dropped.Repaired != true {
		t.Fatal("duplicate was not marked repaired")
	}
	// The dropped fragment's bytes are still traceable through the record.
	if dropped.Offset <= 0 {
		t.Fatalf("duplicate has no source offset: %+v", dropped)
	}
	for _, rt := range repaired {
		if !rt.Mergeable {
			t.Fatalf("track %d wrongly unmergeable: %s", rt.ID, rt.Reason)
		}
	}
}

func TestRepairFixesTfdtRegressionMonotonically(t *testing.T) {
	tracks, _, injected := buildTracks(t, fixture.FaultTfdtRegression)
	report, repaired := transform.Repair(tracks, transform.RepairOptions{})
	if report.Regressions != 1 {
		t.Fatalf("regressions=%d want 1", report.Regressions)
	}
	if len(report.Discontinuities) != 1 {
		t.Fatalf("discontinuities=%+v", report.Discontinuities)
	}
	d := report.Discontinuities[0]
	if d.Kind != "tfdt_regression" || !d.Repaired {
		t.Fatalf("bad discontinuity: %+v", d)
	}
	// The repair must be anchored at the tfdt field the fixture rewound.
	if d.Offset != injected.Injections[0].ByteOffset {
		t.Fatalf("repair offset=%d want %d", d.Offset, injected.Injections[0].ByteOffset)
	}
	for _, rt := range repaired {
		for i := 1; i < len(rt.Samples); i++ {
			if rt.Samples[i].TimelineTime < rt.Samples[i-1].End() && rt.Samples[i].TimelineTime != rt.Samples[i-1].TimelineTime {
				t.Fatalf("track %d still regresses at sample %d", rt.ID, i)
			}
		}
	}
	// Original source times are preserved for every sample.
	for _, rt := range repaired {
		for _, s := range rt.Samples {
			if s.SourceTime < 0 {
				t.Fatalf("source time corrupted")
			}
		}
	}
}

func TestRepairRecordsSequenceGapWithoutMovingTime(t *testing.T) {
	tracks, _, injected := buildTracks(t, fixture.FaultSequenceGap)
	report, repaired := transform.Repair(tracks, transform.RepairOptions{})
	if report.SequenceGaps != 1 {
		t.Fatalf("sequence gaps=%d want 1", report.SequenceGaps)
	}
	d := report.Discontinuities[0]
	if d.Kind != "sequence_gap" {
		t.Fatalf("bad sequence gap record: %+v", d)
	}
	// The record points at the tfdt of the fragment after the missing one;
	// the injection records the same byte.
	if d.Offset != injected.Injections[0].ByteOffset {
		t.Fatalf("sequence gap offset=%d want %d", d.Offset, injected.Injections[0].ByteOffset)
	}
	if d.Repaired {
		t.Fatal("a tfdt-authoritative gap must not be reported as time-repaired")
	}
	for _, rt := range repaired {
		if !rt.Mergeable {
			t.Fatalf("a tfdt-authoritative gap made track unmergeable: %s", rt.Reason)
		}
		for _, s := range rt.Samples {
			if s.Repaired {
				t.Fatalf("sequence gap should not rewrite sample times")
			}
		}
	}
}

func TestRepairMarksUnorderableTrack(t *testing.T) {
	tracks, _, _ := buildTracks(t)
	// Contract a track: a repeat that contradicts its predecessor cannot be
	// ordered without guessing.
	tr := tracks[0]
	if len(tr.Samples) < 4 {
		t.Fatalf("need at least four samples, have %d", len(tr.Samples))
	}
	// Insert a fragment whose sequence repeats its predecessor but whose
	// declaration differs. Insert it right after the same-track fragment so
	// grouping sees the contradiction.
	insertAt := 1
	for i := 1; i < len(tr.Samples); i++ {
		if tr.Samples[i].FragmentSeq != tr.Samples[0].FragmentSeq {
			insertAt = i + 1
			break
		}
	}
	dup := tr.Samples[insertAt-1]
	dup.FragmentIndex = tr.Samples[insertAt-1].FragmentIndex
	dup.FragmentSeq = tr.Samples[insertAt-1].FragmentSeq
	dup.ByteLength++
	dup.TfdtOffset = tr.Samples[insertAt-1].TfdtOffset
	dup.FragmentOffset = tr.Samples[insertAt-1].FragmentOffset
	// Force a distinct fragment index so grouping treats it as its own
	// fragment rather than a continuation of the previous one.
	dup.FragmentIndex = tr.Samples[insertAt-1].FragmentIndex + 1000
	head := append([]timeline.Sample(nil), tr.Samples[:insertAt]...)
	tail := append([]timeline.Sample{dup}, tr.Samples[insertAt:]...)
	tr.Samples = append(head, tail...)
	report, repaired := transform.Repair(tracks, transform.RepairOptions{})
	var found bool
	for _, id := range report.UnmergeableTracks {
		if id == tr.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("unorderable contradiction did not mark the track unmergeable")
	}
	rt := repaired[0]
	if rt.Mergeable {
		t.Fatal("track still marked mergeable")
	}
	if rt.Reason == "" {
		t.Fatal("unmergeable track has no reason")
	}
	if len(rt.Errors) == 0 || rt.Errors[0].Code != fault.CodeUnmergeableTrack {
		t.Fatalf("missing unmergeable fault: %v", rt.Errors)
	}
	if len(report.Discontinuities) == 0 {
		t.Fatal("no discontinuity recorded for the contradiction")
	}
	last := report.Discontinuities[len(report.Discontinuities)-1]
	if last.Kind != "unorderable" {
		t.Fatalf("last discontinuity=%+v", last)
	}
	if last.Offset == 0 {
		t.Fatalf("unorderable fragment has no source offset: %+v", last)
	}
}

func TestRepairIsDeterministic(t *testing.T) {
	kinds := []fixture.FaultKind{fixture.FaultTfdtRegression, fixture.FaultDuplicate}
	tracks1, _, _ := buildTracks(t, kinds...)
	tracks2, _, _ := buildTracks(t, kinds...)
	r1, out1 := transform.Repair(tracks1, transform.RepairOptions{})
	r2, out2 := transform.Repair(tracks2, transform.RepairOptions{})
	if r1.Duplicates != r2.Duplicates || r1.Regressions != r2.Regressions || r1.SequenceGaps != r2.SequenceGaps {
		t.Fatal("repair report differs between identical runs")
	}
	for i := range out1 {
		if len(out1[i].Samples) != len(out2[i].Samples) {
			t.Fatal("repaired sample count differs")
		}
		for j := range out1[i].Samples {
			if out1[i].Samples[j].TimelineTime != out2[i].Samples[j].TimelineTime {
				t.Fatal("repaired times differ between identical runs")
			}
		}
	}
}
