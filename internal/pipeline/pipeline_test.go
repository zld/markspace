package pipeline

import (
	"testing"

	"tsrepair/internal/fixture"
	"tsrepair/internal/mediaerr"
)

func runClean(t *testing.T, corruptions []fixture.Corruption) *Result {
	t.Helper()
	opts := fixture.DefaultOptions(17)
	opts.Segments = 4
	opts.SamplesPerSegment = 8
	opts.Representations = 3
	opts.Corruptions = corruptions
	reps, err := fixture.Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	ec := &mediaerr.Collector{}
	cfg := DefaultConfig()
	res, err := Run(reps, cfg, ec)
	if err != nil {
		t.Fatal(err)
	}
	// Query the actual keyframe start times (from rep 0's segments) so every
	// target lands inside an index range, plus an out-of-range target that
	// must count as a miss.
	var targets []int64
	for _, rpt := range res.Reports {
		if rpt.Segment.Representation == 0 && len(rpt.Segment.KeyframeIndex) > 0 {
			targets = append(targets, rpt.Segment.KeyframeIndex[0].StartNanos)
		}
	}
	// Re-run with the derived targets plus a guaranteed miss.
	cfg.QueryTargetsNanos = append(targets, 1<<62)
	res, err = Run(reps, cfg, &mediaerr.Collector{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCleanRunFullRecovery(t *testing.T) {
	res := runClean(t, nil)
	if res.Metrics.Representations != 3 {
		t.Errorf("representations=%d", res.Metrics.Representations)
	}
	// 4 segments per representation.
	if res.Metrics.Segments != 12 {
		t.Errorf("segments=%d want 12", res.Metrics.Segments)
	}
	if res.Metrics.BoundaryMismatch != 0 {
		t.Errorf("boundary mismatches=%d", res.Metrics.BoundaryMismatch)
	}
	if !res.Metrics.RoundTripOK {
		t.Error("round trip must succeed on clean input")
	}
	// The out-of-range target produces exactly one miss per representation;
	// every keyframe target hits.
	if res.Metrics.IndexHits == 0 {
		t.Error("expected keyframe hits")
	}
	if res.Metrics.IndexMisses != 3 { // one miss per representation
		t.Errorf("misses=%d want 3", res.Metrics.IndexMisses)
	}
	if got := res.Metrics.HitRate(); got >= 1.0 {
		t.Errorf("hit rate should be below 1 with misses, got %f", got)
	}
	// Every report carries a hash.
	for _, r := range res.Reports {
		if len(r.SHA256) != 64 {
			t.Fatal("missing segment hash")
		}
		if !r.Consistent {
			t.Error("clean segments must be boundary-consistent")
		}
	}
}

func TestDeterministicHashesAcrossRuns(t *testing.T) {
	a := runClean(t, nil)
	b := runClean(t, nil)
	if len(a.Reports) != len(b.Reports) {
		t.Fatal("report counts differ")
	}
	for i := range a.Reports {
		if a.Reports[i].SHA256 != b.Reports[i].SHA256 {
			t.Fatalf("segment %d hash differs across runs", i)
		}
	}
}

func TestCorruptionsAreRecoverableAndLocated(t *testing.T) {
	res := runClean(t, []fixture.Corruption{
		fixture.CorruptCounterGap,
		fixture.CorruptPCRWrap,
		fixture.CorruptPSIVersion,
	})
	codes := map[string]bool{}
	for _, e := range res.Errors {
		codes[e.Code] = true
	}
	for _, want := range []string{
		mediaerr.CodeCCGap,
		mediaerr.CodePCRWrap,
		mediaerr.CodeVersionRollback,
	} {
		if !codes[want] {
			t.Errorf("expected error code %s, got %v", want, codes)
		}
	}
}

func TestMissingPCRAnchorReported(t *testing.T) {
	opts := fixture.DefaultOptions(1)
	opts.Corruptions = []fixture.Corruption{fixture.CorruptMissingPCRAnchor}
	reps, _ := fixture.Build(opts)
	ec := &mediaerr.Collector{}
	res, err := Run(reps, DefaultConfig(), ec)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Errors {
		if e.Code == mediaerr.CodeMissingPCRAnchor {
			found = true
		}
	}
	if !found {
		t.Fatal("missing PCR anchor must be reported")
	}
}

func TestTruncatedStreamStructuralFailure(t *testing.T) {
	opts := fixture.DefaultOptions(1)
	opts.Corruptions = []fixture.Corruption{fixture.CorruptTruncatedPacket}
	reps, _ := fixture.Build(opts)
	ec := &mediaerr.Collector{}
	res, err := Run(reps, DefaultConfig(), ec)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Errors {
		if e.Code == mediaerr.CodeTruncatedPacket || e.Code == mediaerr.CodeSyncLost {
			found = true
		}
	}
	if !found {
		t.Fatal("truncation must produce a structured error")
	}
}
