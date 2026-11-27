package stage

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
	"mediad/internal/plan"
)

func writeOutput(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReuseOnlyWhenSummariesMatch(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRunner(dir, "run-1", "alg-1")
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	task := Task{
		Name: "normalize", InputSummary: "in-1", ParamSummary: "p-1",
		Budget: plan.Budget{MaxCPUNanos: 1_000_000, MaxBytes: 1 << 20},
		Run: func(ctx *Context) ([]string, error) {
			runs++
			writeOutput(t, ctx.Dir, "normalized.bin", "data-v1")
			return []string{"normalized.bin"}, nil
		},
	}
	if err := r.Run(task); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("first run executed %d times", runs)
	}
	// Re-deliver identical task: must be skipped, output untouched.
	if err := r.Run(task); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("identical re-delivery re-ran the stage: %d", runs)
	}
	// A changed input summary forces re-execution.
	task.InputSummary = "in-2"
	if err := r.Run(task); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("changed summary must re-run stage, got %d", runs)
	}
}

func TestInterruptIsResumable(t *testing.T) {
	dir := t.TempDir()
	mkTask := func(name string) Task {
		return Task{
			Name: name, InputSummary: "in", ParamSummary: "p",
			Budget: plan.Budget{MaxCPUNanos: 1_000_000, MaxBytes: 1 << 20},
			Run: func(ctx *Context) ([]string, error) {
				writeOutput(t, ctx.Dir, name+".bin", name)
				return []string{name + ".bin"}, nil
			},
		}
	}
	r1, err := NewRunner(dir, "run-x", "alg-1")
	if err != nil {
		t.Fatal(err)
	}
	r1.SetInterruptAfter("normalize")
	if err := r1.Run(mkTask("ingest")); err != nil {
		t.Fatal(err)
	}
	err = r1.Run(mkTask("normalize"))
	if !IsInterrupted(err) {
		t.Fatalf("want interrupted sentinel, got %v", err)
	}
	// State on disk must mark normalize completed.
	st, err := (&Store{Dir: dir}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Stages["normalize"].Status != StatusCompleted {
		t.Fatalf("normalize = %s", st.Stages["normalize"].Status)
	}
	// A fresh runner (simulating process restart) resumes; normalize is reused.
	r2, err := NewRunner(dir, "run-x", "alg-1")
	if err != nil {
		t.Fatal(err)
	}
	normalizedRuns := 0
	export := mkTask("export")
	if err := r2.Run(mkTask("ingest")); err != nil {
		t.Fatal(err)
	}
	_ = normalizedRuns
	if err := r2.Run(mkTask("normalize")); err != nil {
		t.Fatal(err)
	}
	if err := r2.Run(export); err != nil {
		t.Fatal(err)
	}
	if !r2.Reusable(mkTask("normalize")) {
		t.Fatal("normalize must be reusable after resume")
	}
}

func TestOutputHashVerifiedAndDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRunner(dir, "run-h", "alg-1")
	if err != nil {
		t.Fatal(err)
	}
	task := Task{
		Name: "export", InputSummary: "in", ParamSummary: "p",
		Budget: plan.Budget{MaxCPUNanos: 1_000_000, MaxBytes: 1 << 20},
		Run: func(ctx *Context) ([]string, error) {
			writeOutput(t, ctx.Dir, "out.bin", "abc")
			return []string{"out.bin"}, nil
		},
	}
	if err := r.Run(task); err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyReusable(); err != nil {
		t.Fatalf("clean outputs must verify: %v", err)
	}
	writeOutput(t, dir, "out.bin", "tampered")
	err = r.VerifyReusable()
	if model.AsError(err).Code != model.ErrManifestVerify {
		t.Fatalf("want ErrManifestVerify, got %v", err)
	}
}

func TestBudgetExceededFailsResumably(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRunner(dir, "run-b", "alg-1")
	if err != nil {
		t.Fatal(err)
	}
	task := Task{
		Name: "export", InputSummary: "in", ParamSummary: "p",
		Budget: plan.Budget{MaxBytes: 10},
		Run: func(ctx *Context) ([]string, error) {
			ctx.Guard.Add(0, 50) // exceeds byte ceiling
			writeOutput(t, ctx.Dir, "big.bin", "0123456789")
			return []string{"big.bin"}, nil
		},
	}
	err = r.Run(task)
	if model.AsError(err).Code != model.ErrBudgetExceeded {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
	st, _ := (&Store{Dir: dir}).Load()
	if st.Stages["export"].Status != StatusFailed {
		t.Fatalf("status = %s, want failed", st.Stages["export"].Status)
	}
}

func TestCorruptStateRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(dir, "r", "a"); model.AsError(err).Code != model.ErrStateCorrupt {
		t.Fatalf("got %v", err)
	}
}

func TestSummarizeStableAndOrderIndependent(t *testing.T) {
	a := Summarize("x", map[string]string{"b": "2", "a": "1"})
	b := Summarize("x", map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Fatal("map key order must not change the summary")
	}
	c := Summarize("x", map[string]string{"a": "1", "b": "3"})
	if a == c {
		t.Fatal("different inputs must produce different summaries")
	}
}
