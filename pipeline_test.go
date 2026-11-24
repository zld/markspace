package mediacore

import (
	"fmt"
	"io"
	"path/filepath"
	"testing"
)

func acceptanceOptions(dir string) DemoOptions {
	return DemoOptions{
		Seed:      17,
		LossBurst: 3,
		Reorder:   4,
		Duplicate: 2,
		Bandwidth: []int{4000, 900, 2200},
		Repeat:    1,
		WorkDir:   dir,
	}
}

// TestPipelineEndToEndAcceptance drives the same scenario as the acceptance
// command and requires every assertion to pass.
func TestPipelineEndToEndAcceptance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	r := NewDemoRunner(acceptanceOptions(dir), io.Discard)
	report, err := r.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.ExitCode != 0 {
		for _, c := range report.Checks {
			if !c.Pass {
				t.Errorf("failed check %s: %s", c.Name, c.Detail)
			}
		}
		t.Fatalf("ExitCode = %d, want 0", report.ExitCode)
	}
	if report.RoundtripRate != 1 {
		t.Fatalf("roundtrip = %v, want 1", report.RoundtripRate)
	}
	if report.KeyframeHitRate != 1 {
		t.Fatalf("hit rate = %v, want 1", report.KeyframeHitRate)
	}
	if report.DuplicateCount != 2 {
		t.Fatalf("duplicates = %d, want 2", report.DuplicateCount)
	}
	if report.MissingCount != 3 {
		t.Fatalf("missing = %d, want 3", report.MissingCount)
	}
	if !report.IndexRebuilt {
		t.Fatal("first run must build/rebuild the index")
	}
	if len(report.SegmentSHA) != 64 {
		t.Fatalf("segment sha length = %d, want 64", len(report.SegmentSHA))
	}
	if len(report.GapReports) == 0 {
		t.Fatal("expected recorded gap ranges")
	}
	// Both corrupt fixtures must surface as classified container faults.
	if len(report.Faults) != 2 {
		t.Fatalf("fault reports = %d, want 2", len(report.Faults))
	}
	for _, f := range report.Faults {
		if f.Kind != KindContainer || f.Err == "" {
			t.Fatalf("fault %s not a classified container error: %+v", f.Name, f)
		}
	}
	// Every registered check must pass.
	for _, c := range report.Checks {
		if !c.Pass {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

// TestPipelineIdenticalHashesAcrossRuns proves repeat-run determinism.
func TestPipelineIdenticalHashesAcrossRuns(t *testing.T) {
	root := t.TempDir()
	pipe := DefaultPipeline()
	pipe.Scan.SegmentDuration = 1000

	var combined []string
	fx, err := GenerateFixtures(filepath.Join(root, "fx"), 17)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		dir := filepath.Join(root, "runs", fmt.Sprintf("r%d", i))
		res, err := pipe.RunIndexPipeline(fx.MP4, filepath.Join(dir, "cache"), filepath.Join(dir, "out.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		combined = append(combined, res.Hashes.Combined)
	}
	if combined[0] != combined[1] {
		t.Fatalf("hashes differ across runs:\n%s\n%s", combined[0], combined[1])
	}
}

// TestPipelineResumeByDigestAndBoundary verifies interruption resume trusts
// only same-digest, matching segment boundaries.
func TestPipelineResumeByDigestAndBoundary(t *testing.T) {
	p := genFixtures(t, 17)
	c, d, idx := scanFixture(t, p.MP4, 400) // ~0.4s windows -> several segments
	h, err := HashSegments(c, idx.Plan, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Segments) < 3 {
		t.Fatalf("need >=3 segments for resume test, got %d", len(h.Segments))
	}

	if rp := h.ResumeFrom(nil); rp.Segment != 0 {
		t.Fatalf("nil prior must restart at 0, got %d", rp.Segment)
	}

	// A complete prefix of two segments resumes at segment 2.
	prefix := *h
	prefix.Segments = append([]SegmentDigest(nil), h.Segments[:2]...)
	rp := h.ResumeFrom(&prefix)
	if rp.Segment != 2 || rp.Reason != "continuing from digest-bound segment" {
		t.Fatalf("prefix resume = %d (%s), want 2/continuing", rp.Segment, rp.Reason)
	}

	// A hash mismatch inside the prefix restarts at the offending segment.
	bad := prefix
	bad.Segments = append([]SegmentDigest(nil), prefix.Segments...)
	bad.Segments[1].SHA256 = "deadbeef"
	if rp := h.ResumeFrom(&bad); rp.Segment != 1 {
		t.Fatalf("mismatch resume = %d, want 1", rp.Segment)
	}

	// A changed input digest forces a full restart regardless of prefix.
	changed := prefix
	changed.Digest = InputDigest{Content: "different", Size: d.Size}
	if rp := h.ResumeFrom(&changed); rp.Segment != 0 || rp.Reason != "input digest changed" {
		t.Fatalf("changed digest resume = %d (%s), want 0/input digest changed", rp.Segment, rp.Reason)
	}
}

// TestPipelineMarksIncompleteSegmentsForMissingGaps feeds terminal gaps into
// segment hashing and requires the affected segment to be marked incomplete
// rather than silently treated as fully verified.
func TestPipelineMarksIncompleteSegmentsForMissingGaps(t *testing.T) {
	p := genFixtures(t, 17)
	c, d, idx := scanFixture(t, p.MP4, 1000)
	gaps := []Gap{{TrackID: idx.SplitTrackID, FirstSeq: 0, LastSeq: 2, State: GapMissing}}
	h, err := HashSegments(c, idx.Plan, d, gaps)
	if err != nil {
		t.Fatal(err)
	}
	var anyIncomplete bool
	for _, s := range h.Segments {
		if !s.Complete {
			anyIncomplete = true
			if len(s.Missing) == 0 {
				t.Fatal("incomplete segment must carry its missing gaps")
			}
		}
	}
	if !anyIncomplete {
		t.Fatal("expected at least one segment marked incomplete for a missing gap on the split track")
	}
}
