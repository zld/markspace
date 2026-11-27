package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
)

// writeArtifact writes one file into a stage work directory.
func writeArtifact(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFullRunCommitsAllStages(t *testing.T) {
	eng, err := NewEngine(filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]StageFunc{}
	for _, name := range OrderedStages {
		name := name
		funcs[name] = func(_ context.Context, _ *State, _ string, workDir string) error {
			writeArtifact(t, workDir, name+".out", "data-"+name)
			return nil
		}
	}
	r := NewRunner(eng, funcs)
	st, err := r.Run(context.Background(), "src1", "h264")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.NextPending(); got != "" {
		t.Fatalf("all stages should commit, pending=%s", got)
	}
	if st.LastCommitted() != StageArchive {
		t.Fatalf("last committed: %s", st.LastCommitted())
	}
}

func TestInterruptedRunCleansAndResumes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	eng, err := NewEngine(root)
	if err != nil {
		t.Fatal(err)
	}

	// First run: normalize fails after writing a partial artifact.
	failOnce := map[string]int{}
	funcs := map[string]StageFunc{}
	for _, name := range OrderedStages {
		name := name
		funcs[name] = func(_ context.Context, _ *State, _ string, workDir string) error {
			if name == StageNormalize {
				writeArtifact(t, workDir, "partial.tmp", "incomplete")
				if failOnce[name] == 0 {
					failOnce[name]++
					return model.New(model.ErrStageState, "h264", -1, "simulated crash")
				}
			}
			writeArtifact(t, workDir, name+".out", "data-"+name)
			return nil
		}
	}
	r := NewRunner(eng, funcs)
	st, err := r.Run(context.Background(), "src2", "h264")
	if err == nil {
		t.Fatal("first run must fail at normalize")
	}
	if st.IsCommitted(StageNormalize) {
		t.Fatal("normalize must not be committed after crash")
	}
	// The uncommitted partial artifact must remain on disk until rerun.
	leftover := eng.StageDir("src2", StageNormalize)
	if _, err := os.Stat(filepath.Join(leftover, "partial.tmp")); err != nil {
		t.Fatalf("interrupted artifact should exist before rerun: %v", err)
	}

	// Rerun: cleanup must remove the leftover and resume from normalize.
	st2, err := r.Run(context.Background(), "src2", "h264")
	if err != nil {
		t.Fatalf("rerun failed: %v", err)
	}
	if st2.NextPending() != "" {
		t.Fatal("rerun should complete all stages")
	}
	if _, err := os.Stat(leftover); err == nil {
		t.Fatal("uncommitted stage dir must be cleaned after successful retry")
	}
	// No leftover non-committed directories anywhere.
	matches, _ := filepath.Glob(filepath.Join(root, "stages", "src2", "*"))
	for _, m := range matches {
		if filepath.Ext(m) != ".committed" && filepath.Base(m) != filepath.Base(eng.statePath("src2")) {
			// only committed dirs and the state file remain
			if fi, err := os.Stat(m); err == nil && fi.IsDir() && filepath.Ext(m) == "" {
				t.Fatalf("uncommitted dir survived: %s", m)
			}
		}
	}
}

func TestCommittedDigestStableOnRerun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	eng, _ := NewEngine(root)
	funcs := map[string]StageFunc{}
	for _, name := range OrderedStages {
		name := name
		funcs[name] = func(_ context.Context, _ *State, _ string, workDir string) error {
			writeArtifact(t, workDir, name+".out", "stable-"+name)
			return nil
		}
	}
	r := NewRunner(eng, funcs)
	st, err := r.Run(context.Background(), "src3", "av1")
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for name, rec := range st.Stages {
		for fn, d := range rec.Artifacts {
			before[name+"/"+fn] = d
		}
	}

	// Rerun must verify (not rewrite) committed stages and keep digests.
	st2, err := r.Run(context.Background(), "src3", "av1")
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	for name, rec := range st2.Stages {
		for fn, d := range rec.Artifacts {
			if d != before[name+"/"+fn] {
				t.Fatalf("digest changed after rerun for %s/%s", name, fn)
			}
		}
	}
}

func TestVerifyCommittedDetectsTampering(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	eng, _ := NewEngine(root)
	funcs := map[string]StageFunc{
		StageIngest: func(_ context.Context, _ *State, _ string, workDir string) error {
			writeArtifact(t, workDir, "a.out", "original")
			return nil
		},
	}
	// Provide all stages so a clean run can finish.
	for _, name := range OrderedStages[1:] {
		name := name
		funcs[name] = func(_ context.Context, _ *State, _ string, workDir string) error {
			writeArtifact(t, workDir, name+".out", "x")
			return nil
		}
	}
	r := NewRunner(eng, funcs)
	st, err := r.Run(context.Background(), "src4", "h265")
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with a committed artifact.
	p, _ := r.ArtifactPath(st, StageIngest, "a.out")
	if err := os.WriteFile(p, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), "src4", "h265")
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDigestMismatch {
		t.Fatalf("want DIGEST_MISMATCH on tampered artifact, got %v", err)
	}
}

func TestContextCancellationStopsStages(t *testing.T) {
	eng, _ := NewEngine(filepath.Join(t.TempDir(), "work"))
	ctx, cancel := context.WithCancel(context.Background())
	funcs := map[string]StageFunc{
		StageIngest: func(ctx context.Context, _ *State, _ string, workDir string) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}
	for _, name := range OrderedStages[1:] {
		name := name
		funcs[name] = func(_ context.Context, _ *State, _ string, workDir string) error {
			writeArtifact(t, workDir, name+".out", "x")
			return nil
		}
	}
	r := NewRunner(eng, funcs)
	if _, err := r.Run(ctx, "src5", "h264"); err == nil {
		t.Fatal("canceled context must stop the run")
	}
}
