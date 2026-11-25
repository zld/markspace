package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/index"
)

func TestValidationFailurePreservesInputAndDoesNotPublish(t *testing.T) {
	work := t.TempDir()
	input := filepath.Join(work, "source.mp4")
	original := []byte("original-input-bytes")
	if err := os.WriteFile(input, original, 0o644); err != nil {
		t.Fatal(err)
	}
	rn := &Runner{
		WorkDir: work, BatchID: "invalid-batch", ManifestPath: "manifest.json",
		Report: Report{ParseRoundtripRate: 0.5, KeyframeHitRate: 1},
		data:   &analysis{primary: &index.Index{Path: input, Track: 4}},
		State:  &State{Artifacts: map[string]ArtifactState{}},
		Store:  NewStore(filepath.Join(work, "state", "state.json")),
	}
	err := rn.runValidate()
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Category != clock.CategoryInvalidArgument {
		t.Fatalf("validation error = %#v", err)
	}
	if ce.Path != "manifest.json" {
		t.Fatalf("error path = %q, want manifest.json", ce.Path)
	}
	if _, err := os.Stat(filepath.Join(work, "published", "publication.json")); !os.IsNotExist(err) {
		t.Fatalf("publication exists after failed validation: %v", err)
	}
	got, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("original input was modified by failed validation")
	}
}
