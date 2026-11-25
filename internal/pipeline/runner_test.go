package pipeline

import (
	"errors"
	"os"
	"testing"

	"regress-kernel/internal/media"
)

func runProgram(t *testing.T, caseName string, crash bool) (*Workspace, *ProgramResult, error) {
	t.Helper()
	ws := newTestWorkspace(t)
	cfg := media.DefaultConfig(caseName, 17)
	fx := media.Generate(cfg)
	res, err := ws.RunProgram(RunOptions{
		CaseName: caseName, Seed: 17, Fx: fx, CrashAfterWrite: crash,
	})
	return ws, res, err
}

func TestRunProgramNormalMetrics(t *testing.T) {
	ws, res, err := runProgram(t, "normal", false)
	if err != nil {
		t.Fatalf("normal run: %v", err)
	}
	if len(res.Renditions) != 3 {
		t.Fatalf("want 3 renditions, got %d", len(res.Renditions))
	}
	for _, rd := range res.Renditions {
		if rd.SegmentCount != 5 || !rd.Continuous {
			t.Fatalf("rendition %s: count=%d continuous=%v",
				rd.Rendition, rd.SegmentCount, rd.Continuous)
		}
	}
	if res.SyncErrorMs > 20 {
		t.Fatalf("sync error %.3fms exceeds 20ms", res.SyncErrorMs)
	}
	if res.RecoveryRate < 0.95 {
		t.Fatalf("recovery rate %.3f below 0.95", res.RecoveryRate)
	}
	if res.P95LatencyMs <= 0 {
		t.Fatal("p95 latency must be positive")
	}
	if res.ManifestSHA == "" {
		t.Fatal("manifest digest missing")
	}
	// All tasks must end committed.
	keys, _ := ws.SortedTaskKeys()
	if len(keys) != 3 {
		t.Fatalf("want 3 task dirs, got %d", len(keys))
	}
}

func TestRunProgramRerunManifestStable(t *testing.T) {
	ws := newTestWorkspace(t)
	cfg := media.DefaultConfig("normal", 17)
	opts := RunOptions{CaseName: "normal", Seed: 17, Fx: media.Generate(cfg)}
	first, err := ws.RunProgram(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Second run over the same workspace resumes from checkpoints.
	second, err := ws.RunProgram(opts)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !second.Resumed {
		t.Fatal("rerun should resume from a checkpoint")
	}
	if first.ManifestSHA != second.ManifestSHA {
		t.Fatalf("manifest digest drifted: %s vs %s",
			first.ManifestSHA, second.ManifestSHA)
	}
	if first.SyncErrorMs != second.SyncErrorMs {
		t.Fatal("sync metrics must be reproducible")
	}
}

func TestRunProgramCorruptFailsWithFrameIndex(t *testing.T) {
	ws := newTestWorkspace(t)
	cfg := media.DefaultConfig("corrupt", 17)
	cfg.Corrupt = media.Corruption{BadChecksumAt: 7, MissingAt: 15}
	fx := media.Generate(cfg)
	_, err := ws.RunProgram(RunOptions{
		CaseName: "corrupt", Seed: 17, Fx: fx,
	})
	if !IsValidation(err) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatal("error does not carry the frame report")
	}
	if len(ve.Report.Faults) == 0 {
		t.Fatal("bad frame indexes not recorded")
	}
	found := false
	for _, f := range ve.Report.Faults {
		if f.Kind == "bad_hash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a bad_hash fault, got %+v", ve.Report.Faults)
	}
}

func TestRunProgramCrashRecoveryNoNewArtifacts(t *testing.T) {
	ws := newTestWorkspace(t)
	cfg := media.DefaultConfig("crash", 17)
	fx := media.Generate(cfg)

	// First run exits after durable writes, before the merge/commit.
	_, err := ws.RunProgram(RunOptions{
		CaseName: "crash", Seed: 17, Fx: fx, CrashAfterWrite: true,
	})
	if !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("want simulated crash, got %v", err)
	}
	before := countAllSegments(t, ws)

	// Recovery run completes cleanly and must not add any segment.
	res, err := ws.RunProgram(RunOptions{
		CaseName: "crash", Seed: 17, Fx: fx, CrashAfterWrite: false,
	})
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	after := countAllSegments(t, ws)
	if after != before {
		t.Fatalf("artifact count grew: before=%d after=%d", before, after)
	}
	if res.ManifestSHA == "" {
		t.Fatal("recovery did not produce a manifest")
	}
	// A further rerun keeps the same manifest digest.
	res2, err2 := ws.RunProgram(RunOptions{
		CaseName: "crash", Seed: 17, Fx: fx,
	})
	if err2 != nil {
		t.Fatalf("third run errored: %v", err2)
	}
	if res2.ManifestSHA != res.ManifestSHA {
		t.Fatalf("post-recovery manifest unstable: %v", err)
	}
}

func countAllSegments(t *testing.T, ws *Workspace) int {
	t.Helper()
	keys, err := ws.SortedTaskKeys()
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, k := range keys {
		dir, err := ws.SafeJoin("segments", sanitizeKey(k))
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if len(n) > 4 && n[len(n)-4:] == ".m4s" {
				total++
			}
		}
	}
	return total
}
