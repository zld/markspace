package pipeline

import (
	"testing"

	"medpipe/internal/fault"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Seed: 17, BudgetMS: 5000, MaxBytes: 1 << 20, MinQuality: 99,
		OutDir: dir, CheckpointPath: dir + "/checkpoint.gob",
	}
}

func TestRunSelfTestSummaryContract(t *testing.T) {
	cfg := testConfig(t)
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.RoundtripPercent != 100 || s.KeyframeHitPercent != 100 {
		t.Fatalf("rates not full: %+v", s)
	}
	if s.Duplicates != 1 || s.Corrupt != 1 || s.Truncated != 1 || s.Recovered != 1 {
		t.Fatalf("counts wrong: %+v failures=%+v", s, res.Failures)
	}
	if len(s.SegmentSHA256) != 64 {
		t.Fatalf("segment hash %q length %d", s.SegmentSHA256, len(s.SegmentSHA256))
	}
	if len(res.Failures) != 2 {
		t.Fatalf("failures=%+v", res.Failures)
	}
	found := map[string]bool{}
	for _, f := range res.Failures {
		if f.Offset < 0 || f.TrackID < 0 {
			t.Fatalf("failure missing coordinates: %+v", f)
		}
		found[f.Code] = true
	}
	if !found["checksum_mismatch"] {
		t.Fatalf("missing corrupt failure: %+v", res.Failures)
	}
	if !containsTruncated(found) {
		t.Fatalf("missing truncated failure: %+v", res.Failures)
	}
}

func TestDeterministicRerunAndResume(t *testing.T) {
	cfg1 := testConfig(t)
	full, err := Run(cfg1)
	if err != nil {
		t.Fatal(err)
	}

	cfg2 := testConfig(t)
	rerun, err := Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if full.Summary.Line() != rerun.Summary.Line() {
		t.Fatalf("nondeterministic lines:\n%s\n%s", full.Summary.Line(), rerun.Summary.Line())
	}

	resumed, err := Resume(cfg1.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Summary != full.Summary {
		t.Fatalf("resume mismatch:\n%+v\n%+v", resumed.Summary, full.Summary)
	}

	// Simulate a failure immediately after indexing: roll the checkpoint back to
	// the index stage, then rerun budget/render/verify from that coordinate.
	st, err := LoadCheckpoint(cfg1.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Completed = fault.StageIndex
	st.Budget = nil
	st.Manifest = nil
	st.Summary = Summary{}
	rolled := cfg1.OutDir + "/rolled-back.gob"
	if err := SaveCheckpoint(rolled, st); err != nil {
		t.Fatal(err)
	}
	fromIndex, err := Resume(rolled)
	if err != nil {
		t.Fatal(err)
	}
	if fromIndex.Summary != full.Summary {
		t.Fatalf("partial resume mismatch:\n%s\n%s", fromIndex.Summary.Line(), full.Summary.Line())
	}
}

func TestBudgetFailureReturnsErrorAndNoSummaryHash(t *testing.T) {
	cfg := testConfig(t)
	cfg.BudgetMS = 0
	res, err := Run(cfg)
	if err == nil {
		t.Fatal("expected budget failure")
	}
	if res == nil || res.Summary.SegmentSHA256 != "" {
		t.Fatalf("unsuccessful run produced success summary: %+v", res)
	}
}

func containsTruncated(codes map[string]bool) bool {
	for code := range codes {
		if len(code) >= 10 && code[:10] == "truncated_" {
			return true
		}
	}
	return false
}
