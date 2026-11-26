package stream_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"medtrace/internal/core"
	"medtrace/internal/fixture"
	"medtrace/internal/index"
	"medtrace/internal/manifest"
	"medtrace/internal/stream"
)

// dashIndex generates the seeded media tree and indexes the DASH manifest.
func dashIndex(t *testing.T) (*fixture.Scenario, string, *core.Index) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media")
	sc, err := fixture.Build(root, 23)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	pl, err := manifest.Load(root, sc.DashMPD)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	idx, _, err := index.Build(root, pl)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return sc, root, idx
}

func TestRunHealthyProcessesEverything(t *testing.T) {
	sc, root, idx := dashIndex(t)
	res, err := (&stream.Runner{Root: root, Index: idx, VerifyHashes: true}).
		Run(0, sc.DashDuration)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := 7 * sc.SamplesPerSeg; len(res.Processed) != want {
		t.Errorf("processed = %d, want %d", len(res.Processed), want)
	}
	if res.HashChecks != 7 || res.HashMatches != 7 || res.HashBad != 0 {
		t.Errorf("hashes: checks=%d matches=%d bad=%d", res.HashChecks, res.HashMatches, res.HashBad)
	}
	if len(res.Anomalies) != 0 || len(res.Recoveries) != 0 {
		t.Errorf("anomalies=%d recoveries=%d, want none", len(res.Anomalies), len(res.Recoveries))
	}
	last := core.Nanos(-1)
	for _, s := range res.Processed {
		if s.Canonical < 0 || s.Canonical >= sc.DashDuration {
			t.Errorf("sample outside interval at %s", s.Canonical)
		}
		if s.Canonical < last {
			t.Fatalf("processed order regressed at %s", s.Canonical)
		}
		last = s.Canonical
	}
}

func TestRunHonoursHalfOpenInterval(t *testing.T) {
	sc, root, idx := dashIndex(t)
	from, to := core.FromMillis(120), core.FromMillis(240)
	res, err := (&stream.Runner{Root: root, Index: idx, VerifyHashes: true}).Run(from, to)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Processed) != sc.SamplesPerSeg {
		t.Fatalf("processed = %d, want %d", len(res.Processed), sc.SamplesPerSeg)
	}
	for _, s := range res.Processed {
		if s.Canonical < from || s.Canonical >= to {
			t.Errorf("sample at %s outside [%s,%s)", s.Canonical, from, to)
		}
		if s.Segment != "dash/video/v-low/seg-002.m4s" {
			t.Errorf("sample from %s", s.Segment)
		}
	}
}

func TestRunRejectsEmptyInterval(t *testing.T) {
	_, root, idx := dashIndex(t)
	_, err := (&stream.Runner{Root: root, Index: idx}).Run(core.Second, core.Second)
	if err == nil {
		t.Fatal("expected an error for an empty interval")
	}
	if core.KindOf(err) != core.KindUsage {
		t.Errorf("kind = %q, want %q", core.KindOf(err), core.KindUsage)
	}
}

func TestRecoverAfterDamageKeepsOriginalOffsets(t *testing.T) {
	sc, root, idx := dashIndex(t)
	if err := sc.Damage(root); err != nil {
		t.Fatalf("damage: %v", err)
	}
	run := func() *stream.Result {
		res, err := (&stream.Runner{Root: root, Index: idx, VerifyHashes: true}).
			Run(0, sc.DashDuration)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return res
	}
	res := run()

	if res.HashBad != 1 {
		t.Errorf("hash bad = %d, want 1", res.HashBad)
	}
	if len(res.Recoveries) != 1 {
		t.Fatalf("recoveries = %d, want 1", len(res.Recoveries))
	}
	rec := res.Recoveries[0]
	if !rec.Resumed {
		t.Fatal("did not resume after the damaged segment")
	}
	if rec.FailedSegment != sc.CorruptSegment {
		t.Errorf("failed = %q, want %q", rec.FailedSegment, sc.CorruptSegment)
	}
	// The damage is reported at the original offset of the first sample the
	// truncated file can no longer supply, and the resume offset is the
	// keyframe offset recorded in the index; neither is rewritten.
	if rec.FailedByteOffset != sc.CorruptOffset {
		t.Errorf("failed offset = %d, want original %d", rec.FailedByteOffset, sc.CorruptOffset)
	}
	if rec.ResumeSegment != sc.ResumeSegment || rec.ResumeByteOffset != sc.ResumeOffset {
		t.Errorf("resume = %s@%d, want %s@%d",
			rec.ResumeSegment, rec.ResumeByteOffset, sc.ResumeSegment, sc.ResumeOffset)
	}
	if rec.UnrecoveredStart != core.FromMillis(120) || rec.UnrecoveredEnd != core.FromMillis(240) {
		t.Errorf("unrecovered = [%s,%s), want [120ms,240ms)",
			rec.UnrecoveredStart, rec.UnrecoveredEnd)
	}
	// The hash-mismatch anomaly points at the same original offset.
	var mismatch *core.Anomaly
	for i := range res.Anomalies {
		if res.Anomalies[i].Category == core.CatHashMismatch {
			mismatch = &res.Anomalies[i]
		}
	}
	if mismatch == nil {
		t.Fatal("no hash-mismatch anomaly")
	}
	if mismatch.ByteOffset != sc.CorruptOffset || mismatch.Segment != sc.CorruptSegment {
		t.Errorf("mismatch at %s@%d, want %s@%d",
			mismatch.Segment, mismatch.ByteOffset, sc.CorruptSegment, sc.CorruptOffset)
	}
	// No sample from the damaged segment is emitted.
	for _, s := range res.Processed {
		if s.Segment == sc.CorruptSegment {
			t.Fatalf("emitted sample from damaged segment: %+v", s)
		}
	}
	if want := 6 * sc.SamplesPerSeg; len(res.Processed) != want {
		t.Errorf("processed = %d, want %d", len(res.Processed), want)
	}

	// A rerun produces the same order, the same hashes and the same
	// recovery boundaries.
	again := run()
	if !reflect.DeepEqual(res.Processed, again.Processed) {
		t.Error("rerun processed a different sample sequence")
	}
	if !reflect.DeepEqual(res.Recoveries, again.Recoveries) {
		t.Error("rerun produced different recovery boundaries")
	}
	if !reflect.DeepEqual(res.Anomalies, again.Anomalies) {
		t.Error("rerun produced different anomalies")
	}
}
