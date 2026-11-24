package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/fixture"
	"mediad/internal/pipeline"
)

func generateFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	idx, err := fixture.Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Cases) != 5 {
		t.Fatalf("expected 5 cases, got %d", len(idx.Cases))
	}
	return root
}

func TestEndToEndHappyAndDamaged(t *testing.T) {
	root := generateFixture(t)
	state := filepath.Join(t.TempDir(), "state")
	rep, err := pipeline.Run(root, state, pipeline.DefaultParameters())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]pipeline.CaseResult{}
	for _, c := range rep.Cases {
		byID[c.ID] = c
	}
	normal := byID[fixture.CaseNormalDASH]
	if !normal.OK || normal.RoundtripConsistency != 1.0 || normal.KeyframeHitRate != 1.0 {
		t.Fatalf("normal case metrics wrong: %+v", normal)
	}
	if len(normal.SegmentHashes) == 0 {
		t.Fatal("normal case must produce segment hashes")
	}
	hls := byID[fixture.CaseHLSDiscont]
	if !hls.OK || hls.RoundtripConsistency != 1.0 || hls.KeyframeHitRate != 1.0 {
		t.Fatalf("hls case metrics wrong: %+v", hls)
	}
	if hls.DeduplicatedCount == 0 {
		t.Fatal("identical HLS variants must deduplicate")
	}
	// Every damaged case fails, carries a located error and emits no index.
	for _, id := range []string{
		fixture.CaseZeroTimescale, fixture.CaseTruncatedBox, fixture.CaseTimestampFallback,
	} {
		c := byID[id]
		if c.OK {
			t.Fatalf("%s must fail", id)
		}
		if len(c.Errors) != 1 {
			t.Fatalf("%s expected one error, got %d", id, len(c.Errors))
		}
		if len(c.SegmentHashes) != 0 {
			t.Fatalf("%s must not emit hashes/index", id)
		}
	}
	if rep.RoundtripConsistency != 1.0 || rep.KeyframeHitRate != 1.0 {
		t.Fatalf("aggregate metrics wrong: rt=%f kf=%f",
			rep.RoundtripConsistency, rep.KeyframeHitRate)
	}
}

func TestResumeSkipsCommitted(t *testing.T) {
	root := generateFixture(t)
	state := filepath.Join(t.TempDir(), "state")
	first, err := pipeline.Run(root, state, pipeline.DefaultParameters())
	if err != nil {
		t.Fatal(err)
	}
	if first.ResumedCount != 0 {
		t.Fatalf("fresh run resumed count = %d", first.ResumedCount)
	}
	// Capture hashes, then rerun against the same state.
	firstHashes := map[string][]string{}
	for _, c := range first.Cases {
		firstHashes[c.ID] = c.SegmentHashes
	}
	second, err := pipeline.Run(root, state, pipeline.DefaultParameters())
	if err != nil {
		t.Fatal(err)
	}
	if second.ResumedCount != 2 {
		t.Fatalf("resumed count = %d, want 2 committed cases", second.ResumedCount)
	}
	for _, c := range second.Cases {
		if !c.OK {
			if c.Resumed {
				t.Fatalf("failed case %s must not be marked resumed", c.ID)
			}
			continue
		}
		if !c.Resumed {
			t.Fatalf("committed case %s must resume", c.ID)
		}
		if !equalStrings(c.SegmentHashes, firstHashes[c.ID]) {
			t.Fatalf("hashes changed on resume for %s", c.ID)
		}
	}
}

func TestDeterministicAcrossFreshStates(t *testing.T) {
	root := generateFixture(t)
	r1, err := pipeline.Run(root, filepath.Join(t.TempDir(), "s1"), pipeline.DefaultParameters())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := pipeline.Run(root, filepath.Join(t.TempDir(), "s2"), pipeline.DefaultParameters())
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(r1.SegmentHashes, r2.SegmentHashes) {
		t.Fatal("top-level hashes differ across fresh runs")
	}
	if r1.DeduplicatedCount != r2.DeduplicatedCount || r1.SilenceCount != r2.SilenceCount {
		t.Fatal("aggregate counts differ across fresh runs")
	}
}

func TestMissingCasesIndex(t *testing.T) {
	root := t.TempDir()
	_, err := pipeline.Run(root, filepath.Join(t.TempDir(), "s"), pipeline.DefaultParameters())
	if err == nil {
		t.Fatal("missing cases.json must fail")
	}
	if _, statErr := os.Stat(filepath.Join(root, "cases.json")); !os.IsNotExist(statErr) {
		t.Fatal("fixture root must stay untouched on failure")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
