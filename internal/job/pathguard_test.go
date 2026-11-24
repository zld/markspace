package job

import (
	"os"
	"path/filepath"
	"testing"

	"archive-recovery/internal/media"
)

func TestGuardOutputPath(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "source.av1")
	if err := os.WriteFile(input, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "output")

	if err := guardOutputPath("", outside); err != nil {
		t.Fatalf("in-memory input must bypass the guard: %v", err)
	}
	if err := guardOutputPath(input, outside); err != nil {
		t.Fatalf("distinct paths must be allowed: %v", err)
	}
	if err := guardOutputPath(input, input); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("same path must be rejected, got %v", err)
	}
	nested := filepath.Join(outside, "work")
	inside := filepath.Join(nested, "source.av1")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte{9}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := guardOutputPath(inside, nested); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("input inside workdir must be rejected, got %v", err)
	}
}

func TestRunRefusesOverwriteInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "source.av1")
	stream := minimalAV1Stream(t, 6, false)
	if err := os.WriteFile(input, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(JobInput{
		Codec: "av1", ChunkBytes: 64, Seed: 1, Stream: stream,
		InputPath: input, WorkDir: input,
	})
	if media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("run must refuse workdir==input, got %v", err)
	}
}
