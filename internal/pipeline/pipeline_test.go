package pipeline

import (
	"path/filepath"
	"testing"

	"archivecheck/internal/fixture"
	"archivecheck/internal/manifest"
)

func genFixture(t *testing.T, scenario string, window int) (root string, fx *fixture.Fixture) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "fx-"+scenario)
	cfg := fixture.Config{
		Scenario: scenario, Tracks: 3, Window: window,
		BandwidthKbps: []int{800, 420, 1100},
	}
	var err error
	fx, err = fixture.Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	return root, fx
}

func runFresh(t *testing.T, scenario, format string, window int, out string) *RunResult {
	t.Helper()
	root, _ := genFixture(t, scenario, window)
	res, err := Run(RunOptions{
		FixtureRoot: root, Format: format, Window: window,
		MaxAttempts: 1, OutDir: out,
	})
	if err != nil {
		t.Fatalf("pipeline setup failed: %v", err)
	}
	return res
}

func TestValidRunCompletesWithQuantifiedReport(t *testing.T) {
	for _, format := range []string{manifest.FormatHLS, manifest.FormatDASH} {
		t.Run(format, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "frames")
			res := runFresh(t, fixture.ScenarioValid, format, 4, out)
			if res.Status != "completed" {
				t.Fatalf("status = %s, want completed; blocking=%+v", res.Status, res.Blocking)
			}
			if res.LostPackets != 0 || res.CorruptFrames != 0 {
				t.Fatalf("clean fixture must have no errors: %+v", res)
			}
			// 3 tracks * 3 segments * 30 frames = 270 frames.
			if res.FrameCount != 270 || len(res.FrameHashes) != 270 {
				t.Fatalf("frame count = %d hashes=%d", res.FrameCount, len(res.FrameHashes))
			}
			if res.DecodedFrames != 270 {
				t.Fatalf("decoded frames = %d", res.DecodedFrames)
			}
			if res.TargetBitrateKbps <= 0 || res.ActualBitrateKbps <= 0 {
				t.Fatalf("bitrates not quantified: %+v", res)
			}
			if res.SampleCount != 9 {
				t.Fatalf("sample count = %d, want 9", res.SampleCount)
			}
			if res.ManifestAlignMs > 1.0 {
				t.Fatalf("alignment error %.4f ms exceeds 1 ms", res.ManifestAlignMs)
			}
		})
	}
}

func TestRepeatedRunsHashStable(t *testing.T) {
	out1 := filepath.Join(t.TempDir(), "a")
	out2 := filepath.Join(t.TempDir(), "b")
	r1 := runFresh(t, fixture.ScenarioValid, manifest.FormatHLS, 4, out1)
	r2 := runFresh(t, fixture.ScenarioValid, manifest.FormatHLS, 4, out2)
	if r1.FrameCount != r2.FrameCount {
		t.Fatal("frame count differs across runs")
	}
	for i := range r1.FrameHashes {
		if r1.FrameHashes[i] != r2.FrameHashes[i] {
			t.Fatalf("hash %d differs across runs", i)
		}
	}
}

func TestBurstLossBlocksWithLocator(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bad")
	res := runFresh(t, fixture.ScenarioBurstLoss, manifest.FormatHLS, 2, out)
	if res.Status != "blocked" || res.Blocking == nil {
		t.Fatalf("burst loss must block: status=%s", res.Status)
	}
	if res.LostPackets == 0 {
		t.Fatal("lost packet count must be > 0")
	}
	found := false
	for _, id := range res.AffectedTracks {
		if id == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("affected tracks must include track 1: %v", res.AffectedTracks)
	}
	if res.Blocking.Location == "" || res.Blocking.Subsystem != "transport" {
		t.Fatalf("blocking report lacks locator: %+v", res.Blocking)
	}
	// Blocked successors must appear in node states.
	if res.NodeStates["render:frames"] == nil ||
		res.NodeStates["render:frames"].State != "blocked" {
		t.Fatal("render successor should be blocked")
	}
}

func TestTruncatedIncreasesCorruptButCompletes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "trunc")
	validOut := filepath.Join(t.TempDir(), "valid")
	res := runFresh(t, fixture.ScenarioTruncated, manifest.FormatHLS, 4, out)
	valid := runFresh(t, fixture.ScenarioValid, manifest.FormatHLS, 4, validOut)
	if res.TruncatedSegments == 0 {
		t.Fatal("truncated segment count must be reported")
	}
	if res.CorruptFrames <= 0 {
		t.Fatal("truncation must increase corrupt frame count")
	}
	if valid.CorruptFrames != 0 {
		t.Fatal("valid baseline unexpectedly had corrupt frames")
	}
	if res.Status != "degraded" {
		t.Fatalf("truncation is degradable but not blocking, status=%s", res.Status)
	}
}

func TestDuplicateOrderDeduplicatesAndStable(t *testing.T) {
	out1 := filepath.Join(t.TempDir(), "dup1")
	out2 := filepath.Join(t.TempDir(), "dup2")
	r1 := runFresh(t, fixture.ScenarioDuplicateOrder, manifest.FormatHLS, 4, out1)
	r2 := runFresh(t, fixture.ScenarioDuplicateOrder, manifest.FormatHLS, 4, out2)
	if r1.DuplicatePackets == 0 {
		t.Fatal("duplicate packets must be counted")
	}
	if r1.Status != "completed" {
		t.Fatalf("reordered+duplicate input should fully recover: %s", r1.Status)
	}
	if r1.FrameCount != r2.FrameCount {
		t.Fatal("frame count unstable across duplicate-order runs")
	}
	for i := range r1.FrameHashes {
		if r1.FrameHashes[i] != r2.FrameHashes[i] {
			t.Fatal("hashes unstable across duplicate-order runs")
		}
	}
	// Recovered output must match the clean valid timeline hashes.
	validOut := filepath.Join(t.TempDir(), "valid")
	valid := runFresh(t, fixture.ScenarioValid, manifest.FormatHLS, 4, validOut)
	if len(valid.FrameHashes) != len(r1.FrameHashes) {
		t.Fatal("duplicate-order recovery changed frame count")
	}
	for i := range valid.FrameHashes {
		if valid.FrameHashes[i] != r1.FrameHashes[i] {
			t.Fatal("duplicate-order recovery changed frame hashes")
		}
	}
}

func TestResumeRepairsOnlyInvalidatedNodes(t *testing.T) {
	// First run: burst loss blocks and leaves partial state.
	badRoot, _ := genFixture(t, fixture.ScenarioBurstLoss, 2)
	badOut := filepath.Join(t.TempDir(), "bad")
	bad, err := Run(RunOptions{
		FixtureRoot: badRoot, Format: manifest.FormatHLS,
		Window: 2, MaxAttempts: 1, OutDir: badOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Status != "blocked" {
		t.Fatalf("expected blocked baseline, got %s", bad.Status)
	}

	// Repaired corpus (identical layout, intact captures) and resume.
	goodRoot, _ := genFixture(t, fixture.ScenarioRepaired, 4)
	recoveredOut := filepath.Join(t.TempDir(), "recovered")
	rec, err := Run(RunOptions{
		FixtureRoot: goodRoot, Format: manifest.FormatHLS,
		Window: 4, MaxAttempts: 1, OutDir: recoveredOut, ResumeDir: badOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "completed" {
		t.Fatalf("repaired run did not complete: %s blocking=%+v",
			rec.Status, rec.Blocking)
	}
	if rec.RecoveredNodes <= 0 {
		t.Fatalf("recovered node count must be positive: recovered=%d executed=%d",
			rec.RecoveredNodes, rec.ExecutedNodes)
	}
	// Only invalidated nodes and their dependents reran; manifest and
	// unaffected recovery nodes were skipped.
	if rec.ExecutedNodes >= len(rec.NodeStates) {
		t.Fatalf("resume reran everything: executed=%d total=%d",
			rec.ExecutedNodes, len(rec.NodeStates))
	}

	// Hashes must equal a full clean rerun of the valid corpus.
	fullOut := filepath.Join(t.TempDir(), "full")
	full := runFresh(t, fixture.ScenarioValid, manifest.FormatHLS, 4, fullOut)
	if len(full.FrameHashes) != len(rec.FrameHashes) {
		t.Fatal("resume frame count differs from full rerun")
	}
	for i := range full.FrameHashes {
		if full.FrameHashes[i] != rec.FrameHashes[i] {
			t.Fatal("resume hashes differ from full rerun")
		}
	}
}
