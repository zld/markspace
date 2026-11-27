// Processing and validation subsystem tests: drives the full engine over
// self-generated fixtures, including determinism, resume, the four explicit
// worker failure conditions, and the independent validator.
package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/engine"
	"continuity/internal/fixture"
	"continuity/internal/journal"
	"continuity/internal/model"
	"continuity/internal/validate"
)

func runOK(t *testing.T, root string, frames int) *engine.Summary {
	t.Helper()
	spec := fixture.DefaultSpec(frames)
	if _, err := fixture.Generate(root, spec); err != nil {
		t.Fatalf("generate: %v", err)
	}
	s, err := engine.Run(engine.Config{
		Workspace: root, Threads: 4, MaxOutputBytes: 32 << 20, LoudnessTolerance: 1.0,
	})
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	return s
}

func TestFullRunValidatesEveryFrame(t *testing.T) {
	root := t.TempDir()
	s := runOK(t, root, 8)
	if s.Discovered != 8 || s.Planned != 8 || s.Processed != 8 || s.Validated != 8 {
		t.Fatalf("stage counts = %+v, want all 8", s)
	}
	if s.ManifestSHA256 == "" || s.OutputSHA256 == "" {
		t.Fatal("missing manifest/output hashes")
	}
	// Independent re-verification from the files on disk.
	report, _, err := validate.Verify(root, 8, 48000, model.LayoutMono, -23.0)
	if err != nil {
		t.Fatalf("independent verification failed: %v", err)
	}
	if report.Status != "validated" || report.Frames != 8 {
		t.Fatalf("report = %+v", report)
	}
}

func TestDeterministicRepeatedRuns(t *testing.T) {
	root := t.TempDir()
	first := runOK(t, root, 6)
	if first.Comparison != "initial" {
		t.Fatalf("first comparison = %q, want initial", first.Comparison)
	}
	second, err := engine.Run(engine.Config{
		Workspace: root, Threads: 3, MaxOutputBytes: 32 << 20, LoudnessTolerance: 1.0, Resume: true,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Comparison != "identical" || second.ChangedHashes != 0 {
		t.Fatalf("second run = comparison %q changed %d, want identical/0",
			second.Comparison, second.ChangedHashes)
	}
	if second.ManifestSHA256 != first.ManifestSHA256 || second.OutputSHA256 != first.OutputSHA256 {
		t.Fatal("hashes changed across identical runs")
	}
}

func TestResumeAfterInterruptRedoesOnlyUnvalidated(t *testing.T) {
	root := t.TempDir()
	spec := fixture.DefaultSpec(10)
	if _, err := fixture.Generate(root, spec); err != nil {
		t.Fatal(err)
	}
	cfg := engine.Config{
		Workspace: root, Threads: 2, MaxOutputBytes: 32 << 20,
		LoudnessTolerance: 1.0, InterruptAfter: 5,
	}
	if _, err := engine.Run(cfg); err != engine.ErrInterrupted {
		t.Fatalf("interrupt run err = %v, want ErrInterrupted", err)
	}
	jrnl, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if rp := jrnl.ResumePoint(10); rp != 6 {
		t.Fatalf("resume point = %d, want 6", rp)
	}
	cfg.InterruptAfter = 0
	s, err := engine.Run(cfg)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if s.ResumeFrom != 6 || s.Validated != 10 {
		t.Fatalf("resume summary = %+v, want resume 6 validated 10", s)
	}
}

func TestFailedFrameIsTerminalAndCannotValidate(t *testing.T) {
	root := t.TempDir()
	jrnl, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = jrnl.Transition(1, 1, journal.StateProcessing, ""); err != nil {
		t.Fatal(err)
	}
	if err = jrnl.Transition(1, 1, journal.StateFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	// A failed frame must never be promoted straight to validated.
	if err = jrnl.Transition(1, 1, journal.StateValidated, ""); err == nil {
		t.Fatal("failed -> validated transition was allowed")
	}
	if !jrnl.HasFailed() {
		t.Fatal("HasFailed false after a failed transition")
	}
	if err = jrnl.AssertReusable(); err == nil {
		t.Fatal("AssertReusable should reject a workspace with a failed frame")
	}
}

func TestNoFeasibleCandidateDoesNotStartWorkers(t *testing.T) {
	root := t.TempDir()
	spec := fixture.DefaultSpec(6)
	if _, err := fixture.Generate(root, spec); err != nil {
		t.Fatal(err)
	}
	// A byte ceiling of zero rejects every candidate before pool creation.
	_, err := engine.Run(engine.Config{
		Workspace: root, Threads: 2, MaxOutputBytes: 0, LoudnessTolerance: 1.0,
	})
	se, ok := model.AsStructured(err)
	if !ok || se.Stage != model.StagePlanner {
		t.Fatalf("err = %v, want planner structured error", err)
	}
	// No output directory may exist when planning fails.
	if _, statErr := os.Stat(filepath.Join(root, validate.OutputDir)); !os.IsNotExist(statErr) {
		t.Fatal("output directory created despite infeasible plan")
	}
}

func TestTruncatedInputFailsWholeRun(t *testing.T) {
	root := t.TempDir()
	spec := fixture.DefaultSpec(12)
	frames, err := fixture.Generate(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Mutate(root, spec, frames, fixture.MutateTruncate); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Run(engine.Config{
		Workspace: root, Threads: 2, MaxOutputBytes: 32 << 20, LoudnessTolerance: 1.0,
	})
	se, ok := model.AsStructured(err)
	if !ok {
		t.Fatalf("err = %v, want structured error", err)
	}
	if se.Stage != model.StageManifest || se.Frame != 3 || se.Offset < 0 {
		t.Fatalf("anchor = %s/%d/%d, want manifest/3/non-negative", se.Stage, se.Frame, se.Offset)
	}
	// A partial-success manifest must never be reported as finished.
	if _, statErr := os.Stat(filepath.Join(root, validate.OutputDir, validate.ManifestName)); !os.IsNotExist(statErr) {
		t.Fatal("final manifest written despite failed run")
	}
}
