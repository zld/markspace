package pipeline

import (
	"path/filepath"
	"testing"

	"mediasync/internal/index"
	"mediasync/internal/mederr"
)

const (
	testSegments     = 24
	testFramesPerSeg = 32
	testChunk        = 256
)

func testConfig(t *testing.T, resume bool, interrupt int) Config {
	root := t.TempDir()
	return Config{
		WorkspaceRoot:  root,
		WorkDir:        filepath.Join(root, "work"),
		Seed:           23,
		Segments:       testSegments,
		FramesPerSeg:   testFramesPerSeg,
		ChunkRecords:   testChunk,
		MaxRecords:     index.MaxBatchRecords,
		InterruptAfter: interrupt,
		Resume:         resume,
	}
}

func TestFullRunProducesValidSummary(t *testing.T) {
	res, err := Run(testConfig(t, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.IndexRecords < 512 {
		t.Fatalf("index records = %d, want >=512", s.IndexRecords)
	}
	if len(s.IndexSHA256) != 64 {
		t.Fatalf("sha256 length = %d", len(s.IndexSHA256))
	}
	if s.TimestampWraps < 1 {
		t.Fatal("timestamp wraps must propagate to verification")
	}
	if s.VFRIntervals < 3 {
		t.Fatalf("vfr intervals = %d, want >=3", s.VFRIntervals)
	}
	if s.TruncatedSegments < 1 {
		t.Fatal("truncated segment must propagate to verification")
	}
	if !s.InvariantsOK {
		t.Fatal("invariants must pass for generated input")
	}
}

func TestInterruptAndResumeDigestMatch(t *testing.T) {
	// Phase 1: run with an interrupt after the second chunk.
	cfg1 := testConfig(t, false, 2)
	res1, err := Run(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	if !res1.Interrupted {
		t.Fatal("run should stop at the interrupt boundary")
	}
	// Phase 2: resume in the same workspace and finish.
	cfg2 := cfg1
	cfg2.Resume = true
	cfg2.InterruptAfter = 0
	res2, err := Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.RecoveredChunks < testChunk {
		t.Fatalf("recovered records = %d, want >=%d",
			res2.RecoveredChunks, testChunk)
	}
	if !res2.Summary.CheckpointRecovered {
		t.Fatal("resume must report checkpoint recovery")
	}
	// Phase 3: a clean full run in a fresh workspace must yield the exact
	// same index digest as the interrupted+resumed run.
	full, err := Run(testConfig(t, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	if full.Summary.IndexSHA256 != res2.Summary.IndexSHA256 {
		t.Fatalf("digest mismatch:\nresumed=%s\nfull   =%s",
			res2.Summary.IndexSHA256, full.Summary.IndexSHA256)
	}
	if full.Summary.IndexRecords != res2.Summary.IndexRecords {
		t.Fatal("record counts must match between resume and full runs")
	}
}

func TestFreshRunIgnoresStaleCheckpoints(t *testing.T) {
	cfg := testConfig(t, false, 2)
	if _, err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	// A non-resume run against the same workspace starts cleanly and
	// produces the same digest as an untouched full run.
	again, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again.Interrupted {
		// Re-interrupting is expected; now finish without resume.
		cfg.InterruptAfter = 0
		again, err = Run(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	full, err := Run(testConfig(t, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	if again.Summary.IndexSHA256 != full.Summary.IndexSHA256 {
		t.Fatal("fresh run must not inherit stale checkpoint state")
	}
}

func TestExitCodePaths(t *testing.T) {
	// The pipeline surfaces typed errors; exit-code mapping is owned by
	// mederr, assert the three mandated classes are reachable.
	cases := map[mederr.ErrorCode]int{
		mederr.CodeInvalidManifest:   mederr.ExitInvalidInput,
		mederr.CodeUnrecoverableGap:  mederr.ExitUnrecoverable,
		mederr.CodeInternal:          mederr.ExitInternal,
	}
	for code, want := range cases {
		if got := mederr.ExitCode(mederr.New(code, "probe")); got != want {
			t.Fatalf("code %s -> exit %d, want %d", code, got, want)
		}
	}
}
