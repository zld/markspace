package pipeline

import (
	"testing"
)

func TestNormalScenario(t *testing.T) {
	m, err := Run(Config{Case: "normal", OutDir: t.TempDir(), Policy: PolicyMarkGaps})
	if err != nil {
		t.Fatalf("normal run: %v", err)
	}
	if m.RoundtripOK != 8 || m.RoundtripTotal != 8 {
		t.Fatalf("roundtrip = %d/%d, want 8/8", m.RoundtripOK, m.RoundtripTotal)
	}
	if m.KeyframeHits != 8 || m.KeyframeTotal != 8 {
		t.Fatalf("keyframe_hit = %d/%d, want 8/8", m.KeyframeHits, m.KeyframeTotal)
	}
	if m.Segments != 4 || m.HashMismatch != 0 {
		t.Fatalf("segments=%d mismatch=%d, want 4/0", m.Segments, m.HashMismatch)
	}
	if len(m.SegmentHashes) != 4 {
		t.Fatalf("want four segment hashes, got %d", len(m.SegmentHashes))
	}
	for _, sh := range m.SegmentHashes {
		if len(sh.SHA256) != 64 {
			t.Fatalf("segment hash must be 64 hex chars, got %q", sh.SHA256)
		}
	}
}

func TestNormalHashesDeterministicAcrossRuns(t *testing.T) {
	m1, err := Run(Config{Case: "normal", OutDir: t.TempDir(), Policy: PolicyMarkGaps})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Run(Config{Case: "normal", OutDir: t.TempDir(), Policy: PolicyMarkGaps})
	if err != nil {
		t.Fatal(err)
	}
	for i := range m1.SegmentHashes {
		if m1.SegmentHashes[i].SHA256 != m2.SegmentHashes[i].SHA256 {
			t.Fatalf("segment %d hash differs across runs", i+1)
		}
	}
}

func TestDamagedScenarioMarkGaps(t *testing.T) {
	m, err := Run(Config{Case: "damaged", OutDir: t.TempDir(), Policy: PolicyMarkGaps})
	if err != nil {
		t.Fatalf("damaged run under mark-gaps must complete: %v", err)
	}
	if m.RoundtripOK != 7 || m.RoundtripTotal != 7 {
		t.Fatalf("roundtrip = %d/%d, want 7/7", m.RoundtripOK, m.RoundtripTotal)
	}
	if m.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", m.Rejected)
	}
	if m.Gaps != 1 {
		t.Fatalf("gap = %d, want 1", m.Gaps)
	}
	if m.KeyframeHits != 8 || m.KeyframeTotal != 8 {
		t.Fatalf("keyframe_hit = %d/%d, want 8/8", m.KeyframeHits, m.KeyframeTotal)
	}
}

func TestConflictStrictRollsBack(t *testing.T) {
	m, err := Run(Config{Case: "conflict", OutDir: t.TempDir(), Policy: PolicyStrict})
	if err == nil {
		t.Fatal("strict conflict must return an error")
	}
	if m.Conflicts != 1 || m.Rollbacks != 1 {
		t.Fatalf("conflicts=%d rollback=%d, want 1/1", m.Conflicts, m.Rollbacks)
	}
	if m.ManifestChanged {
		t.Fatal("manifest must remain unchanged after rollback")
	}
}

func TestRecoveryInterruptAndResume(t *testing.T) {
	dir := t.TempDir()
	// First run is interrupted after two staged segments.
	_, err := Run(Config{Case: "recovery", OutDir: dir, Policy: PolicyMarkGaps,
		InterruptAfter: 2})
	if err == nil {
		t.Fatal("interrupted run must surface the simulated interrupt")
	}
	// Resume completes the transaction.
	m, err := Resume(dir)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if m.Replayed != 0 || m.DuplicateCues != 0 {
		t.Fatalf("replayed=%d duplicate_cues=%d, want 0/0", m.Replayed, m.DuplicateCues)
	}
	matches, total, err := VerifyResumeHashes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if matches != 4 || total != 4 {
		t.Fatalf("hash_match = %d/%d, want 4/4", matches, total)
	}
	if m.Segments != 4 {
		t.Fatalf("resumed segments = %d, want 4", m.Segments)
	}
}

func TestResumeMatchesUninterruptedHashes(t *testing.T) {
	dirClean := t.TempDir()
	clean, err := Run(Config{Case: "normal", OutDir: dirClean, Policy: PolicyMarkGaps})
	if err != nil {
		t.Fatal(err)
	}
	dirCrash := t.TempDir()
	_, _ = Run(Config{Case: "recovery", OutDir: dirCrash, Policy: PolicyMarkGaps,
		InterruptAfter: 2})
	resumed, err := Resume(dirCrash)
	if err != nil {
		t.Fatal(err)
	}
	for i := range clean.SegmentHashes {
		if clean.SegmentHashes[i].SHA256 != resumed.SegmentHashes[i].SHA256 {
			t.Fatalf("segment %d resumed hash differs from uninterrupted", i+1)
		}
	}
}
