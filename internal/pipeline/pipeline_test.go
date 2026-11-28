package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"medialab/internal/fixtures"
	"medialab/internal/model"
)

// tempConfig points a run at a fresh directory so tests never touch the user's
// temporary space and are easy to inspect on failure.
func tempConfig(t *testing.T, caseName string) Config {
	t.Helper()
	return Config{
		Case: caseName, Seed: 7,
		BudgetMS: 500, MaxBytes: 1 << 20, Workers: 2,
		CacheBytes: 24 << 20, ReorderWin: 8,
		MaxDim: model.MaxDimension, MinQuality: 0.5,
		OutDir: t.TempDir(), KeepInput: false,
	}
}

func runCase(t *testing.T, cfg Config) *Result {
	t.Helper()
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func TestRunNormalSucceeds(t *testing.T) {
	res := runCase(t, tempConfig(t, fixtures.CaseNormal))
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, faults = %v", res.ExitCode, res.Faults)
	}
	if res.Render == nil || res.Verify == nil {
		t.Fatal("pipeline did not reach render and verify")
	}
	if res.Render.Manifest.Summary.Frames == 0 {
		t.Fatal("no frames exported")
	}
	// The verifier re-derives the run hash and must agree with the manifest.
	if res.Render.Manifest.Summary.RunHash != res.Verify.RunHash {
		t.Fatalf("run hashes differ: %s vs %s",
			res.Render.Manifest.Summary.RunHash, res.Verify.RunHash)
	}
	if res.Verify.MaxAlignNS != 0 {
		t.Fatalf("unexpected alignment error %d", res.Verify.MaxAlignNS)
	}
}

func TestRunDeterministic(t *testing.T) {
	var hashes []string
	for i := 0; i < 2; i++ {
		res := runCase(t, tempConfig(t, fixtures.CaseNormal))
		if res.ExitCode != 0 {
			t.Fatalf("run %d exit = %d", i, res.ExitCode)
		}
		hashes = append(hashes, res.Render.Manifest.Summary.RunHash)
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("run hashes differ across identical runs: %s vs %s", hashes[0], hashes[1])
	}
}

func TestRunCorruptHashExitsOne(t *testing.T) {
	res := runCase(t, tempConfig(t, fixtures.CaseCorruptHash))
	if res.ExitCode != 1 {
		t.Fatalf("exit = %d want 1", res.ExitCode)
	}
	var found *model.Fault
	for _, f := range res.Faults {
		if f.Code == model.CodeHashMismatch {
			found = f
		}
	}
	if found == nil {
		t.Fatalf("no hash mismatch fault: %v", res.Faults)
	}
	if found.Track != 2 || found.Frame != 2 {
		t.Fatalf("fault names track %d frame %d", found.Track, found.Frame)
	}
}

func TestRunReorderLossRecovers(t *testing.T) {
	res := runCase(t, tempConfig(t, fixtures.CaseReorderLoss))
	if res.ExitCode != 0 {
		t.Fatalf("recoverable case exited %d: %v", res.ExitCode, res.Faults)
	}
	fills := res.Render.Manifest.Summary.FillFrames
	if fills == 0 {
		t.Fatal("reorder-loss produced no fill frames")
	}
	// Every track must still complete; a recoverable gap never drops a track.
	tracks := map[model.TrackID]bool{}
	for _, f := range res.Render.Manifest.Frames {
		tracks[f.Track] = true
	}
	if len(tracks) != res.Dataset.Spec.Tracks {
		t.Fatalf("only %d of %d tracks completed", len(tracks), res.Dataset.Spec.Tracks)
	}
	if res.Verify.MaxAlignNS != 0 {
		t.Fatalf("alignment error %d", res.Verify.MaxAlignNS)
	}
}

func TestRunTimestampRegressionWarns(t *testing.T) {
	res := runCase(t, tempConfig(t, fixtures.CaseTimestampRegression))
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d: %v", res.ExitCode, res.Faults)
	}
	var regression bool
	for _, w := range res.Warnings {
		if w.Code == model.CodeTimeRegression {
			regression = true
		}
	}
	if !regression {
		t.Fatalf("timestamp regression not reported: %v", res.Warnings)
	}
}

func TestRunBudgetConflictExitsOne(t *testing.T) {
	cfg := tempConfig(t, fixtures.CaseBudgetConflict)
	cfg.BudgetMS = 1
	cfg.MaxBytes = 64
	res := runCase(t, cfg)
	if res.ExitCode != 1 {
		t.Fatalf("exit = %d want 1", res.ExitCode)
	}
	if res.Plan == nil || res.Plan.Conflict == nil {
		t.Fatal("no conflict recorded")
	}
	if len(res.Plan.Conflict.Evaluated) == 0 {
		t.Fatal("conflict has no candidate summary")
	}
	if res.Render != nil {
		t.Fatal("render ran despite an infeasible plan")
	}
}

func TestRunCacheBoundaryRespectsCap(t *testing.T) {
	cfg := tempConfig(t, fixtures.CaseCacheBoundary)
	cfg.CacheBytes = 100_000
	cfg.Workers = 4
	res := runCase(t, cfg)
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d: %v", res.ExitCode, res.Faults)
	}
	peak := res.Render.Manifest.Summary.PeakCacheBytes
	if peak == 0 {
		t.Fatal("peak cache not recorded")
	}
	if peak > cfg.CacheBytes {
		t.Fatalf("peak cache %d exceeds cap %d", peak, cfg.CacheBytes)
	}
}

func TestRunLeavesArtefacts(t *testing.T) {
	cfg := tempConfig(t, fixtures.CaseNormal)
	res := runCase(t, cfg)
	// The manifest and at least one segment must be readable after the run.
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	for _, s := range res.Render.Manifest.Segments {
		if _, err := os.Stat(filepath.Join(res.OutDir, s.Path)); err != nil {
			t.Fatalf("segment missing: %v", err)
		}
	}
}

// TestRerunKeepsCompletedTrackHashes encodes the partial-success contract: a
// run with recoverable gaps can be repeated, and every frame a track actually
// rendered keeps its hash. Fill frames are separately comparable too.
func TestRerunKeepsCompletedTrackHashes(t *testing.T) {
	first := runCase(t, tempConfig(t, fixtures.CaseReorderLoss))
	second := runCase(t, tempConfig(t, fixtures.CaseReorderLoss))
	if first.ExitCode != 0 || second.ExitCode != 0 {
		t.Fatalf("exits = %d, %d", first.ExitCode, second.ExitCode)
	}
	index := func(res *Result) map[string]string {
		out := map[string]string{}
		for _, f := range res.Render.Manifest.Frames {
			key := trackFrameKey(f.Track, f.Sequence)
			if f.Fill {
				key += "/fill"
			}
			out[key] = f.SHA256
		}
		return out
	}
	a, b := index(first), index(second)
	if len(a) != len(b) {
		t.Fatalf("frame counts differ: %d vs %d", len(a), len(b))
	}
	for key, hash := range a {
		if b[key] != hash {
			t.Fatalf("%s hash changed across rerun: %s vs %s", key, hash, b[key])
		}
	}
	// Render every completed (non-fill) frame's hash differs from any fill, so
	// this really did compare rendered content.
	if first.Render.Manifest.Summary.FillFrames == 0 {
		t.Fatal("case produced no fills; nothing was actually recovered")
	}
}

func trackFrameKey(track model.TrackID, seq uint32) string {
	return fmt.Sprintf("%d/%d", int(track), seq)
}
