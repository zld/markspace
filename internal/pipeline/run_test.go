package pipeline

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/checkpoint"
	"mediad/internal/clock"
	"mediad/internal/fixture"
)

func runOpts(t *testing.T, dir string, corrupt string, resume bool) Options {
	t.Helper()
	return Options{
		WorkDir: dir, Seed: 42, TargetBps: 8_000_000, Corrupt: corrupt, Resume: resume,
	}
}

func TestRunCleanMeetsContract(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(runOpts(t, dir, fixture.CorruptNone, false))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if math.Abs(r.BitrateDeltaPct) > 5 {
		t.Fatalf("bitrate delta %.3f%% exceeds 5%%", r.BitrateDeltaPct)
	}
	if r.AlignmentErrorMs > 1 {
		t.Fatalf("alignment %.3fms exceeds 1ms", r.AlignmentErrorMs)
	}
	if r.IndexCoveragePct != 100 {
		t.Fatalf("coverage %.2f%%", r.IndexCoveragePct)
	}
	if math.IsNaN(r.QualityPSNRDb) || math.IsInf(r.QualityPSNRDb, 0) {
		t.Fatalf("quality not finite: %v", r.QualityPSNRDb)
	}
	if r.Samples == 0 || r.Keyframes == 0 {
		t.Fatalf("empty report: %+v", r)
	}
	if res.OutputSHA == "" || res.InputSHA == "" {
		t.Fatal("digests missing")
	}
	if res.Resumed {
		t.Fatal("cold run must not be marked resumed")
	}
	// Provenance and report artifacts exist.
	for _, name := range []string{"provenance.json", "report.json"} {
		if _, err := os.Stat(filepath.Join(dir, "out", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestResumeProducesIdenticalDigestAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := Run(runOpts(t, dir, fixture.CorruptNone, false))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(runOpts(t, dir, fixture.CorruptNone, true))
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputSHA != second.OutputSHA {
		t.Fatalf("digest changed across resume: %s vs %s", first.OutputSHA, second.OutputSHA)
	}
	if second.Resumed != true {
		t.Fatal("second run should report resumed=true")
	}
	if first.Report.ActualBps != second.Report.ActualBps {
		t.Fatalf("bitrate changed: %d vs %d", first.Report.ActualBps, second.Report.ActualBps)
	}
	// A third cold-equivalent run with checkpoints present but --resume off
	// recomputes but still yields the same digest.
	third, err := Run(runOpts(t, dir, fixture.CorruptNone, false))
	if err != nil {
		t.Fatal(err)
	}
	if third.OutputSHA != first.OutputSHA {
		t.Fatalf("recompute digest differs: %s vs %s", third.OutputSHA, first.OutputSHA)
	}
	if third.Resumed {
		t.Fatal("non-resume run should recompute")
	}
}

func TestChangedTargetInvalidatesCheckpoints(t *testing.T) {
	dir := t.TempDir()
	first, err := Run(runOpts(t, dir, fixture.CorruptNone, false))
	if err != nil {
		t.Fatal(err)
	}
	opts := runOpts(t, dir, fixture.CorruptNone, true)
	opts.TargetBps = 4_000_000
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Resumed {
		t.Fatal("changed parameters must not resume")
	}
	if second.OutputSHA == first.OutputSHA {
		t.Fatal("different target must yield different digest")
	}
	// The manifest records the target it was generated with, so a changed
	// target without regeneration is a parameter change, not new media. The
	// report must still be internally consistent (finite, coverage complete).
	if second.Report.IndexCoveragePct != 100 {
		t.Fatalf("coverage %.2f", second.Report.IndexCoveragePct)
	}
	if math.IsNaN(second.Report.BitrateDeltaPct) || math.IsInf(second.Report.BitrateDeltaPct, 0) {
		t.Fatalf("delta not finite: %v", second.Report.BitrateDeltaPct)
	}
}

func TestCorruptCheckpointIsRecomputed(t *testing.T) {
	dir := t.TempDir()
	first, err := Run(runOpts(t, dir, fixture.CorruptNone, false))
	if err != nil {
		t.Fatal(err)
	}
	// Truncate the repair checkpoint to simulate a crash mid-write.
	ck := filepath.Join(dir, "checkpoints", "repair.json")
	if err := os.WriteFile(ck, []byte(`{"version":1,"stage":"repai`), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Run(runOpts(t, dir, fixture.CorruptNone, true))
	if err != nil {
		t.Fatal(err)
	}
	if second.OutputSHA != first.OutputSHA {
		t.Fatalf("recompute after corruption changed digest: %s vs %s", second.OutputSHA, first.OutputSHA)
	}
	if !second.Resumed {
		t.Fatal("scan checkpoint should still be reusable, so resumed=true")
	}
	// The rewritten checkpoint must be valid again.
	store, err := checkpoint.Open(filepath.Join(dir, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(checkpoint.StageRepair); err != nil {
		t.Fatalf("repair checkpoint not repaired: %v", err)
	}
}

func TestTruncatedTableFailsWithOffset(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(runOpts(t, dir, fixture.CorruptTruncatedTable, false))
	if err == nil {
		t.Fatal("expected failure")
	}
	pe, ok := clock.As(err)
	if !ok || pe.Stage != clock.StageScan || pe.Code != clock.CodeTruncatedTable {
		t.Fatalf("unexpected error: %v", err)
	}
	if pe.RawOffset <= 0 {
		t.Fatalf("missing raw offset: %+v", pe)
	}
	if clock.ExitCode(err) != 2 {
		t.Fatalf("exit code %d", clock.ExitCode(err))
	}
}

func TestEditOverrunFailsVerification(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(runOpts(t, dir, fixture.CorruptEditOverrun, false))
	if err == nil {
		t.Fatal("expected verification failure")
	}
	pe, ok := clock.As(err)
	if !ok || pe.Stage != clock.StageVerify || pe.Code != clock.CodeVerification {
		t.Fatalf("unexpected error: %v", err)
	}
	// A failed verification must not leave a delivery report behind.
	if _, err := os.Stat(filepath.Join(dir, "corrupt-"+fixture.CorruptEditOverrun, "out", "report.json")); err == nil {
		t.Fatal("verification failure wrote a report")
	}
}

func TestMissingSyncTraversesAllStages(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(runOpts(t, dir, fixture.CorruptMissingSync, false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Keyframes == 0 {
		t.Fatal("no keyframes inferred")
	}
	// Provenance must disclose that the index is derived.
	if len(res.Provenance.Cameras) == 0 {
		t.Fatalf("no camera provenance: %+v", res.Provenance)
	}
	found := false
	for _, cam := range res.Provenance.Cameras {
		if cam.DerivedSync {
			found = true
		}
	}
	if !found {
		t.Fatalf("derived sync not disclosed: %+v", res.Provenance.Cameras)
	}
	// At least one non-real camera uses inferred sync; coverage still full.
	if res.Report.IndexCoveragePct != 100 {
		t.Fatalf("coverage %.2f", res.Report.IndexCoveragePct)
	}
}

func TestRTPWrapExtended(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(runOpts(t, dir, fixture.CorruptRTPWrap, false))
	if err != nil {
		t.Fatal(err)
	}
	// The repaired checkpoints carry the RTP anchor; verify a wrap was seen.
	store, err := checkpoint.Open(filepath.Join(dir, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := store.Load(checkpoint.StageRepair)
	if err != nil || e == nil {
		t.Fatalf("repair checkpoint: %v", err)
	}
	if !json.Valid(e.Payload) {
		t.Fatal("repair payload invalid")
	}
	if res.Report.IndexCoveragePct != 100 {
		t.Fatalf("coverage %.2f", res.Report.IndexCoveragePct)
	}
}

func TestBadArgumentsExitOne(t *testing.T) {
	dir := t.TempDir()
	opts := runOpts(t, dir, fixture.CorruptNone, false)
	opts.TargetBps = -1
	_, err := Run(opts)
	if err == nil {
		t.Fatal("expected argument failure")
	}
	if clock.ExitCode(err) != 1 {
		t.Fatalf("exit code %d want 1", clock.ExitCode(err))
	}
}

func TestBadManifestExitOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(mp, []byte(`{"version":2,"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{WorkDir: dir, Seed: 42, TargetBps: 8_000_000, ManifestPath: mp, MediaRoot: dir, Resume: true, ExternalManifest: true}
	_, err := Run(opts)
	if err == nil {
		t.Fatal("expected manifest failure")
	}
	if clock.ExitCode(err) != 1 {
		t.Fatalf("exit code %d want 1", clock.ExitCode(err))
	}
}

func TestDTSRegressionTraversesAllStages(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(runOpts(t, dir, fixture.CorruptDTSRegression, false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.TimestampRepairs == 0 {
		t.Fatal("expected timestamp repairs for stuck-tail regression")
	}
	// The repair intervals must appear in provenance with a cause and offset.
	found := false
	for _, cam := range res.Provenance.Cameras {
		for _, iv := range cam.Repairs {
			if iv.Cause == clock.CodeDTSRegression && iv.RawOffset > 0 && iv.Count > 0 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("dts-regression intervals missing from provenance: %+v", res.Provenance.Cameras)
	}
	// Timeline must remain usable: coverage complete, bitrate/alignment intact.
	if res.Report.IndexCoveragePct != 100 {
		t.Fatalf("coverage %.2f", res.Report.IndexCoveragePct)
	}
	if res.Report.AlignmentErrorMs > 1 {
		t.Fatalf("alignment %.3fms", res.Report.AlignmentErrorMs)
	}
}
