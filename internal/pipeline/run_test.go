package pipeline_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/plan"
)

func TestCheckpointResumeAndRepeatHasZeroDiff(t *testing.T) {
	root := t.TempDir()
	fx, err := fixture.Generate(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	bitrates := []int64{900_000, 400_000} // intentionally unsorted

	_, err = pipeline.Run(pipeline.Config{
		InputPath: fx.ValidInput, OutputDir: out, Bitrates: bitrates,
		ResumeAfter: 1, Log: &bytes.Buffer{},
	})
	if k := apperrors.KindOf(err); k != apperrors.KindInterrupted {
		t.Fatalf("checkpoint err kind=%q want %q: %v", k, apperrors.KindInterrupted, err)
	}

	r2, err := pipeline.Run(pipeline.Config{
		InputPath: fx.ValidInput, OutputDir: out, Bitrates: bitrates, Log: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if r2.Manifest.TransitionCount != 5 {
		t.Fatalf("transitions=%d want5", r2.Manifest.TransitionCount)
	}
	if r2.Resume.StartStage != plan.StageScanned {
		t.Fatalf("resume start=%s want scanned", r2.Resume.StartStage)
	}
	if r2.Manifest.MaxBoundaryDiffTicks > 1 {
		t.Fatalf("boundary diff=%d >1", r2.Manifest.MaxBoundaryDiffTicks)
	}
	if len(r2.Manifest.Assets[0].Layers) != 2 {
		t.Fatalf("layers=%d want2", len(r2.Manifest.Assets[0].Layers))
	}

	r3, err := pipeline.Run(pipeline.Config{
		InputPath: fx.ValidInput, OutputDir: out, Bitrates: bitrates, Log: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("repeat run: %v", err)
	}
	if r3.RepeatManifestByteDiff != 0 {
		t.Fatalf("manifest byte diff=%d want0", r3.RepeatManifestByteDiff)
	}
	if r3.RepeatArtifactDigestDiff != 0 {
		t.Fatalf("artifact digest diff=%d want0", r3.RepeatArtifactDigestDiff)
	}
	if r3.ManifestSHA256 != r2.ManifestSHA256 {
		t.Fatal("manifest SHA changed on repeat")
	}
}

func TestChangedInputCreatesNewRecord(t *testing.T) {
	root := t.TempDir()
	fx, err := fixture.Generate(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	cfg := pipeline.Config{InputPath: fx.ValidInput, OutputDir: out, Bitrates: []int64{400_000, 900_000}}
	old, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}

	m := fixture.ValidManifest()
	m.Description = "changed input digest"
	b, err := model.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(filepath.Dir(fx.ValidInput), "changed.json")
	if err := os.WriteFile(changed, b, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.InputPath = changed
	newRes, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if newRes.Manifest.RecordID == old.Manifest.RecordID {
		t.Fatal("changed input reused old record")
	}
	if newRes.Manifest.InputSHA256 == old.Manifest.InputSHA256 {
		t.Fatal("input digest did not change")
	}
	for _, a := range newRes.Manifest.Artifacts {
		if _, err := os.Stat(filepath.Join(out, a.Path)); err != nil {
			t.Fatalf("new artifact missing: %v", err)
		}
	}
	if _, err := os.Stat(old.JournalPath); err != nil {
		t.Fatalf("old journal should remain: %v", err)
	}
}

func TestResumeAfterEveryTransitionIsRecoverable(t *testing.T) {
	root := t.TempDir()
	fx, err := fixture.Generate(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			out := filepath.Join(root, "out-"+string(rune('0'+n)))
			bitrates := []int64{400_000, 900_000}
			_, err := pipeline.Run(pipeline.Config{
				InputPath: fx.ValidInput, OutputDir: out, Bitrates: bitrates,
				ResumeAfter: n, Log: &bytes.Buffer{},
			})
			if k := apperrors.KindOf(err); k != apperrors.KindInterrupted {
				t.Fatalf("checkpoint %d kind=%q: %v", n, k, err)
			}
			r, err := pipeline.Run(pipeline.Config{
				InputPath: fx.ValidInput, OutputDir: out, Bitrates: bitrates,
				Log: &bytes.Buffer{},
			})
			if err != nil {
				t.Fatalf("resume checkpoint %d: %v", n, err)
			}
			if r.Manifest.Status != string(plan.StageCompleted) || r.Manifest.TransitionCount != 5 {
				t.Fatalf("checkpoint %d result status=%s transitions=%d", n, r.Manifest.Status, r.Manifest.TransitionCount)
			}
			repeat, err := pipeline.Run(pipeline.Config{
				InputPath: fx.ValidInput, OutputDir: out, Bitrates: bitrates,
				Log: &bytes.Buffer{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if repeat.RepeatManifestByteDiff != 0 || repeat.RepeatArtifactDigestDiff != 0 {
				t.Fatalf("checkpoint %d repeat diffs manifest=%d artifacts=%d",
					n, repeat.RepeatManifestByteDiff, repeat.RepeatArtifactDigestDiff)
			}
		})
	}
}
