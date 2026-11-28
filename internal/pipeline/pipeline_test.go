package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/pipeline"
)

func silent() func(pipeline.Event) { return func(pipeline.Event) {} }

func TestFullRunThenVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	res, err := pipeline.Run(pipeline.Options{OutDir: dir, Seed: 17}, silent())
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	if res.Stage != model.StageVerified || res.Interrupted {
		t.Fatalf("unexpected result stage=%s interrupted=%t", res.Stage, res.Interrupted)
	}
	if len(res.Layers) != 2 {
		t.Fatalf("layers = %d, want 2", len(res.Layers))
	}
	for _, l := range res.Layers {
		if l.Frames != 12 || !l.Complete || l.OpenGaps != 0 {
			t.Fatalf("layer not fully recovered: %+v", l)
		}
	}
	if res.MaxAlignTicks > 1 {
		t.Fatalf("alignment error %d > 1 tick", res.MaxAlignTicks)
	}
	vr, err := pipeline.Verify(dir, 2)
	if err != nil || !vr.OK || vr.RepeatDifferences != 0 {
		t.Fatalf("verify failed: %+v err=%v", vr, err)
	}
	// Sealed manifest and checkpoint exist on disk.
	for _, f := range []string{manifest.FileManifest, manifest.FileChecksum, manifest.FileCheckpoint} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("expected %s on disk: %v", f, err)
		}
	}
}

func TestInterruptThenResumeCompletes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	first, err := pipeline.Run(pipeline.Options{
		OutDir: dir, Seed: 17, InterruptAfter: 2,
	}, silent())
	if err != nil {
		t.Fatalf("interrupted run: %v", err)
	}
	if !first.Interrupted || first.Stage != model.StageExported {
		t.Fatalf("expected an exported interruption, got %+v", first)
	}
	for _, l := range first.Layers {
		// Two of three segments anchor: frames 0..7.
		if l.LastSegment != 1 || l.LastFrame != 7 || l.Frames != 8 {
			t.Fatalf("interrupt anchor wrong: %+v", l)
		}
		if l.OpenGaps == 0 {
			t.Fatal("interrupted run must retain open gaps for resume")
		}
	}
	// No manifest may be sealed before completion.
	if _, err := os.Stat(filepath.Join(dir, manifest.FileManifest)); !os.IsNotExist(err) {
		t.Fatalf("manifest must not exist after interruption, err=%v", err)
	}

	resumed, err := pipeline.Run(pipeline.Options{OutDir: dir, Resume: true}, silent())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Stage != model.StageVerified {
		t.Fatalf("resume did not reach verified: %s", resumed.Stage)
	}
	for _, l := range resumed.Layers {
		if l.Frames != 12 || l.LastSegment != 2 || l.LastFrame != 11 {
			t.Fatalf("resume anchor wrong: %+v", l)
		}
		if !l.Complete || l.RecoveredGaps == 0 {
			t.Fatalf("resume must recover gaps: %+v", l)
		}
	}
	vr, err := pipeline.Verify(dir, 1)
	if err != nil || !vr.OK {
		t.Fatalf("post-resume verify failed: %+v err=%v", vr, err)
	}
}

func TestCorruptInputWritesNothing(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "guarded")
	_, err := pipeline.Run(pipeline.Options{
		OutDir: dir, Seed: 17, Corrupt: "mxf-klv-length",
	}, silent())
	fe, ok := model.AsField(err)
	if !ok || (fe.Kind != model.KindTruncated && fe.Kind != model.KindOutOfRange) {
		t.Fatalf("expected boundary FieldError, got %v", err)
	}
	if fe.Offset < 0 || fe.Path == "" {
		t.Fatalf("corrupt error lacks offset/path: %+v", fe)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatal("corrupt input must not create the output directory")
	}
}

func TestRerunProducesIdenticalManifest(t *testing.T) {
	d1 := filepath.Join(t.TempDir(), "a")
	d2 := filepath.Join(t.TempDir(), "b")
	r1, err := pipeline.Run(pipeline.Options{OutDir: d1, Seed: 17}, silent())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := pipeline.Run(pipeline.Options{OutDir: d2, Seed: 17}, silent())
	if err != nil {
		t.Fatal(err)
	}
	if r1.ManifestHash == "" || r1.ManifestHash != r2.ManifestHash {
		t.Fatalf("manifest hashes differ: %s vs %s", r1.ManifestHash, r2.ManifestHash)
	}
}

func TestVerifiedArtifactCannotBeOverwritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if _, err := pipeline.Run(pipeline.Options{OutDir: dir, Seed: 17}, silent()); err != nil {
		t.Fatal(err)
	}
	_, err := pipeline.Run(pipeline.Options{OutDir: dir, Seed: 17}, silent())
	if !model.IsKind(err, model.KindInvalidState) {
		t.Fatalf("expected invalid-state guard, got %v", err)
	}
}
