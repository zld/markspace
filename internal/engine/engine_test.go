package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/dag"
	"mediad/internal/fixture"
	"mediad/internal/model"
)

func generateStream(t *testing.T, corrupt string) *fixture.Stream {
	t.Helper()
	spec := fixture.DefaultSpec()
	spec.Corrupt = corrupt
	st, err := fixture.Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCleanRunProducesAlignedManifest(t *testing.T) {
	dir := t.TempDir()
	st := generateStream(t, fixture.CorruptNone)
	rep, err := Run(context.Background(), Config{OutDir: dir, Input: st})
	if err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if rep.RepresentationCount < 2 {
		t.Fatalf("representation_count = %d, want >= 2", rep.RepresentationCount)
	}
	if rep.SegmentCount == 0 {
		t.Fatal("no segments produced")
	}
	if rep.MaxAlignDeltaTicks > st.PacketTicks {
		t.Fatalf("alignment delta %d exceeds one packet duration %d",
			rep.MaxAlignDeltaTicks, st.PacketTicks)
	}
	if len(rep.ManifestSHA256) != 64 {
		t.Fatalf("bad manifest digest %q", rep.ManifestSHA256)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	// Init fragments exist for every representation.
	for i := 0; i < rep.RepresentationCount; i++ {
		name := repID(i) + "-init.mp4"
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("init fragment %s missing: %v", name, err)
		}
	}
	// No temp files survive a successful run.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 4 && e.Name()[:4] == ".tmp" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestRepeatedRunIsDeterministic(t *testing.T) {
	st := generateStream(t, fixture.CorruptNone)
	dir1, dir2 := t.TempDir(), t.TempDir()
	r1, err := Run(context.Background(), Config{OutDir: dir1, Input: st})
	if err != nil {
		t.Fatal(err)
	}
	// A second, independent run over the same generated stream.
	st2 := generateStream(t, fixture.CorruptNone)
	r2, err := Run(context.Background(), Config{OutDir: dir2, Input: st2})
	if err != nil {
		t.Fatal(err)
	}
	if r1.ManifestSHA256 != r2.ManifestSHA256 {
		t.Fatalf("manifest digest differs across identical runs:\n%s\n%s",
			r1.ManifestSHA256, r2.ManifestSHA256)
	}
	m1, _ := os.ReadFile(filepath.Join(dir1, "manifest.json"))
	m2, _ := os.ReadFile(filepath.Join(dir2, "manifest.json"))
	if string(m1) != string(m2) {
		t.Fatal("manifest bytes differ across identical runs")
	}
}

func TestCorruptionReportedWithOffsetAndPID(t *testing.T) {
	cases := []struct {
		mode string
	}{
		{fixture.CorruptSync},
		{fixture.CorruptTruncated},
		{fixture.CorruptContinuity},
		{fixture.CorruptPCR},
		{fixture.CorruptPSI},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			st := generateStream(t, tc.mode)
			rep, err := Run(context.Background(), Config{OutDir: dir, Input: st})
			if err != nil {
				t.Fatalf("engine should report corruption in-band, got %v", err)
			}
			if rep.CorruptionOffset < 0 {
				t.Fatal("no corruption offset reported")
			}
			delta := rep.CorruptionOffset - st.CorruptionOffset
			if delta < 0 {
				delta = -delta
			}
			if delta > model.PacketSize {
				t.Fatalf("corruption offset %d differs from generator %d by %d",
					rep.CorruptionOffset, st.CorruptionOffset, delta)
			}
			if rep.AffectedPID == 0 && tc.mode != fixture.CorruptSync && tc.mode != fixture.CorruptTruncated {
				t.Fatalf("affected pid missing for %s", tc.mode)
			}
		})
	}
}

func TestInterruptAndResumeProducesIdenticalManifest(t *testing.T) {
	st := generateStream(t, fixture.CorruptNone)
	dir := t.TempDir()

	// First run stops after two segments are committed.
	first, err := Run(context.Background(), Config{
		OutDir: dir, Input: st, InterruptAt: "segment:2",
	})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	if first.SegmentCount != 2 {
		t.Fatalf("interrupted run committed %d segments, want 2", first.SegmentCount)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("interrupted run must not write a manifest")
	}
	// The checkpoint exists and holds the committed segments.
	cp, err := dag.LoadCheckpoint(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		t.Fatalf("checkpoint missing after interrupt: %v", err)
	}
	if got := len(cp.Commits()); got != 2*first.RepresentationCount {
		t.Fatalf("ledger entries = %d, want %d", got, 2*first.RepresentationCount)
	}

	// A full uninterrupted run over identical input is the reference.
	refDir := t.TempDir()
	ref, err := Run(context.Background(), Config{OutDir: refDir, Input: generateStream(t, fixture.CorruptNone)})
	if err != nil {
		t.Fatal(err)
	}

	// Resume from the checkpoint. InterruptAt is cleared by passing a new
	// Config that points at the same output directory.
	resumed, err := Run(context.Background(), Config{OutDir: dir, Input: st})
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	// Two segments were committed before the interrupt, so resume starts at
	// segment index 2.
	if resumed.ResumedFromSegment != 2 {
		t.Fatalf("resumed_from_segment = %d, want 2", resumed.ResumedFromSegment)
	}
	if resumed.ResumeMs > 5000 {
		t.Fatalf("resume_ms = %d exceeds 5000", resumed.ResumeMs)
	}
	if resumed.ManifestSHA256 != ref.ManifestSHA256 {
		t.Fatalf("resumed manifest %s != reference %s",
			resumed.ManifestSHA256, ref.ManifestSHA256)
	}
	if resumed.SegmentCount != ref.SegmentCount {
		t.Fatalf("segment count after resume %d != reference %d",
			resumed.SegmentCount, ref.SegmentCount)
	}
	// The ledger must not contain duplicate commits for any task key.
	keys := map[string]int{}
	for _, c := range cp.Commits() {
		keys[c.Key]++
	}
	for k, n := range keys {
		if n != 1 {
			t.Fatalf("task %s committed %d times", k, n)
		}
	}
}

func TestBackpressureHonorsContextCancel(t *testing.T) {
	// A tiny queue plus an immediately cancelled context must release the
	// producer rather than deadlock when consumers stop draining.
	dir := t.TempDir()
	st := generateStream(t, fixture.CorruptNone)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Config{OutDir: dir, Input: st})
	if err == nil {
		t.Fatal("cancelled run must return an error")
	}
}
