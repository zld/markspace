package stage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/manifest"
)

var names = []string{"a", "b", "c", "d"}

func mkManifest() *manifest.Manifest {
	return manifest.New(1, 10000, 0, names)
}

func recorder(seen *[]string, failAt map[string]error) []Step {
	var steps []Step
	for _, name := range names {
		n := name
		steps = append(steps, Step{
			Name: n,
			Run: func(ctx context.Context) error {
				*seen = append(*seen, n)
				return failAt[n]
			},
		})
	}
	return steps
}

func TestRunAllStages(t *testing.T) {
	var seen []string
	r := New(recorder(&seen, nil), Options{}, mkManifest())
	out, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 4 {
		t.Fatalf("ran %d stages", len(seen))
	}
	if out.ResumeStage != "complete" {
		t.Fatalf("resume = %s", out.ResumeStage)
	}
	if len(out.Completed) != 4 {
		t.Fatalf("completed = %v", out.Completed)
	}
}

func TestSoftFailureContinues(t *testing.T) {
	soft := fault.At(fault.StageRTCP, "parse", 42, fault.ErrTruncated)
	var seen []string
	r := New(recorder(&seen, map[string]error{"b": soft}), Options{}, mkManifest())
	out, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("soft failure should not abort: %v", err)
	}
	if len(seen) != 4 {
		t.Fatalf("ran %d stages, want 4", len(seen))
	}
	if len(out.Partial) != 1 || out.Partial[0] != "b" {
		t.Fatalf("partial = %v", out.Partial)
	}
	if len(out.Failures) != 1 || !errors.Is(out.Failures[0], fault.ErrTruncated) {
		t.Fatalf("failures = %v", out.Failures)
	}
	if out.ResumeStage != "b" {
		t.Fatalf("resume = %s, want b (partial is not complete)", out.ResumeStage)
	}
}

func TestHardFailureStops(t *testing.T) {
	fatal := fault.New(fault.StageMux, "write", os.ErrPermission)
	var seen []string
	r := New(recorder(&seen, map[string]error{"b": fatal}), Options{}, mkManifest())
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("hard failure should abort")
	}
	if len(seen) != 2 {
		t.Fatalf("ran %v, want a,b only", seen)
	}
}

func TestInterruptAndResume(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")

	var first []string
	r := New(recorder(&first, nil), Options{StatePath: state, InterruptAfter: "b"}, mkManifest())
	out, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Interrupted != "b" {
		t.Fatalf("interrupted = %q", out.Interrupted)
	}
	if len(first) != 2 {
		t.Fatalf("ran %v, want a,b", first)
	}

	var second []string
	r2 := New(recorder(&second, nil), Options{StatePath: state, Resume: true}, mkManifest())
	out2, err := r2.Run(context.Background())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(second) != 2 || second[0] != "c" || second[1] != "d" {
		t.Fatalf("resume ran %v, want c,d", second)
	}
	if len(out2.Skipped) != 2 || out2.Skipped[0] != "a" {
		t.Fatalf("skipped = %v", out2.Skipped)
	}
	if out2.ResumeStage != "complete" {
		t.Fatalf("resume stage = %s", out2.ResumeStage)
	}
}

func TestResumeWhenComplete(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	var seen []string
	r := New(recorder(&seen, nil), Options{StatePath: state}, mkManifest())
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen = nil
	r2 := New(recorder(&seen, nil), Options{StatePath: state, Resume: true}, mkManifest())
	out, err := r2.Run(context.Background())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("complete run should skip everything, ran %v", seen)
	}
	if out.ResumeStage != "complete" {
		t.Fatalf("resume = %s", out.ResumeStage)
	}
}

func TestCancelBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var seen []string
	r := New(recorder(&seen, nil), Options{}, mkManifest())
	_, err := r.Run(ctx)
	if !errors.Is(err, fault.ErrBudget) {
		t.Fatalf("error = %v, want budget", err)
	}
	if len(seen) != 0 {
		t.Fatalf("no stage should run, ran %v", seen)
	}
}

func TestBudgetDeadline(t *testing.T) {
	start := time.Unix(0, 0)
	dl, ok := BudgetDeadline(start, 10000)
	if !ok || dl.Sub(start) != 10*time.Second {
		t.Fatalf("deadline = %v, %v", dl, ok)
	}
	if _, ok := BudgetDeadline(start, 0); ok {
		t.Fatal("zero budget should mean no deadline")
	}
}

func TestCheckpointAtomicity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := WriteAtomic(context.Background(), path, []byte("hello")); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read back = %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestLoadCheckpointMissing(t *testing.T) {
	cp, err := LoadCheckpoint(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing checkpoint should be empty: %v", err)
	}
	if len(cp.Completed) != 0 {
		t.Fatalf("checkpoint = %+v", cp)
	}
}
