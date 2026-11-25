package mediakernel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunDemoHappyPathAndQuantifiedJSON(t *testing.T) {
	dir := t.TempDir()
	res, err := RunDemo(context.Background(), DemoOptions{
		Seed: 7, QueueCap: 8, SlowConsumerMs: 2, WorkDir: dir,
	})
	if err != nil {
		t.Fatalf("clean demo must succeed: %v", err)
	}
	if res.Counters.Packets == 0 {
		t.Fatal("packets must be positive")
	}
	if res.Counters.LostPackets == 0 || res.Counters.Duplicates == 0 {
		t.Fatal("fixture must show losses and duplicates")
	}
	if res.Counters.QueuePeak > 8 {
		t.Fatal("queue peak must not exceed configured capacity")
	}
	if res.Migration == nil || res.Migration.Stage != StageDone || !res.Migration.Committed {
		t.Fatalf("migration must be committed: %+v", res.Migration)
	}
	if res.Plan.RecomputeCount == 0 || res.Plan.ReuseCount != 0 {
		t.Fatal("first run recomputes all assets")
	}
	if !res.SpecPass || len(res.Failures) != 0 {
		t.Fatalf("clean demo must pass spec: %+v", res.Failures)
	}
	if res.Bitrate.TargetBps != FixtureTargetBitrateBps || res.Bitrate.RelativeDeviation == 0 && res.Bitrate.ActualBps == 0 {
		t.Fatal("bitrate spec must carry target and a numeric deviation")
	}
	if res.Alignment.Checked != 2 || res.Alignment.Failed != 0 {
		t.Fatalf("both in-tolerance subtitles must pass: %+v", res.Alignment)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFileName)); err != nil {
		t.Fatal("manifest must be persisted")
	}
}

func TestRunDemoSecondRunReusesAssets(t *testing.T) {
	dir := t.TempDir()
	opts := DemoOptions{Seed: 7, QueueCap: 8, WorkDir: dir}
	if _, err := RunDemo(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	res2, err := RunDemo(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Plan.ReuseCount == 0 || res2.Plan.RecomputeCount != 0 {
		t.Fatalf("unchanged second run must reuse all assets: %+v", res2.Plan)
	}
}

func TestRunDemoCorruptReportsOffsetPhase(t *testing.T) {
	for _, mode := range []CorruptMode{CorruptTruncated, CorruptInvalidDuration, CorruptSequence} {
		dir := t.TempDir()
		res, err := RunDemo(context.Background(), DemoOptions{
			Seed: 7, WorkDir: dir, Corrupt: mode,
		})
		if err == nil {
			t.Fatalf("corrupt mode %s must fail", mode)
		}
		if res == nil || res.Error == nil {
			t.Fatalf("mode %s must return a structured error in the result", mode)
		}
		if res.Error.Offset < 0 || res.Error.Phase == "" || res.Error.Reason == "" {
			t.Fatalf("mode %s error must carry offset/phase/reason: %+v", mode, res.Error)
		}
		if ClassifyExitCode(err) != ExitRuntimeError {
			t.Fatalf("mode %s is a runtime error (exit 2)", mode)
		}
	}
}

func TestRunDemoInterruptKeepsOriginalManifest(t *testing.T) {
	dir := t.TempDir()
	// Establish an original committed manifest.
	first, err := RunDemo(context.Background(), DemoOptions{Seed: 7, WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}

	res, ierr := RunDemo(context.Background(), DemoOptions{
		Seed: 7, WorkDir: dir, InterruptAfter: StageStage,
	})
	if ierr == nil {
		t.Fatal("interrupted migration must return an error")
	}
	if res == nil || res.Error == nil || res.Error.Phase != PhaseMigrate {
		t.Fatal("interrupt must be a migration-phase structured error")
	}
	after, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil || string(after) != string(originalBytes) {
		t.Fatal("original manifest must be byte-identical after interrupt")
	}

	// A normal follow-up run commits again with an identical digest.
	recovered, err := RunDemo(context.Background(), DemoOptions{Seed: 7, WorkDir: dir})
	if err != nil {
		t.Fatalf("recovery run must succeed: %v", err)
	}
	if !recovered.Migration.ManifestDigest.Equal(*first.Migration.ManifestDigest) {
		t.Fatal("recovered manifest digest must match the original")
	}
}

func TestVerifyRepeatStability(t *testing.T) {
	dir := t.TempDir()
	if _, err := RunDemo(context.Background(), DemoOptions{Seed: 7, WorkDir: dir}); err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), dir, 2)
	if err != nil {
		t.Fatalf("verify must pass: %v", err)
	}
	if !v.Stable || !v.SpecPass || v.ManifestDigest.Hex == "" || v.IndexDigest.Hex == "" {
		t.Fatalf("verification must be stable and fully populated: %+v", v)
	}
}

func TestVerifyAdaptsLegacyV1(t *testing.T) {
	dir := t.TempDir()
	if _, err := RunDemo(context.Background(), DemoOptions{
		Seed: 7, WorkDir: dir, ManifestVersion: ManifestV1,
	}); err != nil {
		t.Fatal(err)
	}
	// The manifest left on disk must genuinely be V1.
	m, err := LoadManifest(filepath.Join(dir, ManifestFileName))
	if err != nil {
		t.Fatalf("loading a v1 manifest must adapt, got %v", err)
	}
	if m.Version != ManifestCurrent {
		t.Fatalf("decoded/adapted version = %d, want %d", m.Version, ManifestCurrent)
	}
	v, err := Verify(context.Background(), dir, 1)
	if err != nil {
		t.Fatalf("verify over adapted v1 must pass: %v", err)
	}
	if !v.AdaptedFromV1 || !v.Stable {
		t.Fatalf("verify must report v1 adaptation and stability: %+v", v)
	}
}
