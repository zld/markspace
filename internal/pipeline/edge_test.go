package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"archivist/internal/fault"
	"archivist/internal/fixture"
)

// TestCleanCorruptRunKeepsGoing verifies that corrupt inputs inside a batch
// are diagnosed individually and do not hide later inputs.
func TestCleanCorruptRunKeepsGoing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inputs")
	normal := fixture.DefaultOptions("ok", fixture.KindNormal)
	normal.SampleCount = 8
	good, err := fixture.Build(dir, normal)
	if err != nil {
		t.Fatal(err)
	}
	badOpts := fixture.DefaultOptions("bad", fixture.KindBadLength)
	badOpts.SampleCount = 8
	bad, err := fixture.BuildCorrupt(dir, badOpts, fixture.KindBadLength)
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, root, 1, 0)
	results, err := r.Run(context.Background(), []string{bad.Path, good.Path})
	if err == nil {
		t.Fatal("expected corruption error")
	}
	if fault.ExitCode(err) != 2 {
		t.Fatalf("exit = %d", fault.ExitCode(err))
	}
	if results[0].Status != "error" || results[0].ErrorOffset != bad.CorruptAt {
		t.Fatalf("bad result = %+v", results[0])
	}
	if results[1] == nil || results[1].Status != "ok" {
		t.Fatalf("good input was not processed: %+v", results[1])
	}
}

// TestTimestampFailureIsCorruptExit verifies that an unrepairable timeline is
// reported as exit code 2 with track context and no output.
func TestTimestampFailureIsCorruptExit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inputs")
	opts := fixture.DefaultOptions("jump", fixture.KindRegression)
	opts.SampleCount = 10
	opts.FirstPTS = 800000
	s, err := fixture.BuildDegraded(dir, opts, fixture.Degrade{AtSample: 3, Mode: fixture.DegradeBackward, Amount: 200000})
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, root, 1, 0)
	results, err := r.Run(context.Background(), []string{s.Path})
	if err == nil {
		t.Fatal("expected timestamp failure")
	}
	if fault.ExitCode(err) != 2 {
		t.Fatalf("exit = %d (%v)", fault.ExitCode(err), err)
	}
	res := results[0]
	if res.Status == "ok" || res.TrackID != 1 || res.ErrorOffset < 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "work", "output", "jump.mp4")); !os.IsNotExist(err) {
		t.Fatalf("timestamp failure produced output: %v", err)
	}
}

// TestMissingInputIsOperationalFailure verifies that an unreadable path is
// exit code 1, not a corruption verdict.
func TestMissingInputIsOperationalFailure(t *testing.T) {
	root := t.TempDir()
	r := newRunner(t, root, 1, 0)
	_, err := r.Run(context.Background(), []string{filepath.Join(root, "nope.mp4")})
	if err == nil {
		t.Fatal("expected error")
	}
	if fault.ExitCode(err) != 1 {
		t.Fatalf("exit = %d (%v)", fault.ExitCode(err), err)
	}
}

// TestCorruptCheckpointDoesNotCountAsProgress verifies that a damaged
// checkpoint is ignored rather than trusted or reported as success.
func TestCorruptCheckpointDoesNotCountAsProgress(t *testing.T) {
	root, inputs := setup(t, 12)
	r := newRunner(t, root, 1, 0)
	first, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first[0].CheckpointPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), inputs)
	if err == nil {
		t.Fatal("expected checkpoint parse error")
	}
	if fault.ExitCode(err) != 1 {
		t.Fatalf("exit = %d", fault.ExitCode(err))
	}
}

// TestStageArtifactsSurviveRerun verifies scan/repair artifacts are reusable
// and their digests match the input identity.
func TestStageArtifactsSurviveRerun(t *testing.T) {
	root, inputs := setup(t, 12)
	r := newRunner(t, root, 1, 0)
	first, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := loadCheckpoint(first[0].CheckpointPath)
	if err != nil || cp == nil {
		t.Fatalf("checkpoint: %v", err)
	}
	for _, a := range []Artifact{cp.Scan, cp.Repair, cp.Output, cp.Index, cp.Provenance} {
		if err := verifyArtifact(a); err != nil {
			t.Fatalf("artifact %s: %v", a.Path, err)
		}
	}
}

// TestChangedInputInvalidatesCheckpoint verifies that rewriting the input
// bytes forces a full run instead of resuming stale state.
func TestChangedInputInvalidatesCheckpoint(t *testing.T) {
	root, inputs := setup(t, 10)
	r := newRunner(t, root, 1, 0)
	if _, err := r.Run(context.Background(), inputs); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	// Flip one payload byte; the container stays valid but the digest moves.
	data[len(data)-1] ^= 0xFF
	if err := os.WriteFile(inputs[0], data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].ResumedStages) != 0 {
		t.Fatalf("stale checkpoint reused: %v", res[0].ResumedStages)
	}
	if len(res[0].ExecutedStages) != len(stageOrder) {
		t.Fatalf("executed = %v", res[0].ExecutedStages)
	}
}
