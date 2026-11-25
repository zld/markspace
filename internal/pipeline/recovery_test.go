package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/fixture"
	"mediacore/internal/index"
	"mediacore/internal/pipeline"
)

func testRange() index.Range {
	return index.Range{Start: 600_000_000_000, End: 612_000_000_000}
}

func prepareManifest(t *testing.T) string {
	t.Helper()
	set, err := fixture.GenerateNormal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return set.ManifestPath
}

func TestCrashAfterPublishRecoversWithoutDuplicate(t *testing.T) {
	manifest := prepareManifest(t)
	work := t.TempDir()
	rn := pipeline.NewRunner(work, manifest, "test-batch", testRange())
	rn.CrashAfter = "publish"
	first, err := rn.Run()
	if !errors.Is(err, pipeline.ErrSimulatedCrash) {
		t.Fatalf("first run err = %v, want simulated crash", err)
	}
	pubPath := filepath.Join(work, "published", "publication.json")
	if _, err := os.Stat(pubPath); err != nil {
		t.Fatalf("publication was not made durable before crash: %v", err)
	}

	rn2 := pipeline.NewRunner(work, manifest, "test-batch", testRange())
	rep, err := rn2.Run()
	if err != nil {
		t.Fatal(err)
	}
	if rep.RecoveredArtifacts != 1 {
		t.Fatalf("recovered = %d, want 1", rep.RecoveredArtifacts)
	}
	if rep.DuplicatePublications != 0 || rep.PublishWrites != 0 {
		t.Fatalf("publish writes=%d duplicates=%d, want 0/0", rep.PublishWrites, rep.DuplicatePublications)
	}
	if len(rep.SegmentHashes) != 3 {
		t.Fatalf("segment hashes = %v", rep.SegmentHashes)
	}
	if first == nil || len(first.SegmentHashes) != 3 || first.SegmentHashes[0] != rep.SegmentHashes[0] {
		t.Fatalf("hashes changed across recovery: first=%v recovered=%v", first, rep.SegmentHashes)
	}

	rn3 := pipeline.NewRunner(work, manifest, "test-batch", testRange())
	again, err := rn3.Run()
	if err != nil {
		t.Fatal(err)
	}
	if again.RecoveredArtifacts != 0 || again.PublishWrites != 0 || again.DuplicatePublications != 0 {
		t.Fatalf("second rerun counters = %+v", again)
	}
	if again.SegmentHashes[1] != rep.SegmentHashes[1] {
		t.Fatal("segment hash changed on clean rerun")
	}
}

func TestCommittedStateWithMissingOrCorruptProductIsRegenerated(t *testing.T) {
	manifest := prepareManifest(t)
	work := t.TempDir()
	if _, err := pipeline.NewRunner(work, manifest, "repair-batch", testRange()).Run(); err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(work, "published", "publication.json")

	if err := os.Remove(pubPath); err != nil {
		t.Fatal(err)
	}
	rep, err := pipeline.NewRunner(work, manifest, "repair-batch", testRange()).Run()
	if err != nil {
		t.Fatal(err)
	}
	if rep.PublishWrites != 1 || rep.RegeneratedArtifacts != 1 {
		t.Fatalf("missing product repair = writes:%d regenerated:%d", rep.PublishWrites, rep.RegeneratedArtifacts)
	}
	if _, err := os.Stat(pubPath); err != nil {
		t.Fatalf("product not restored: %v", err)
	}

	if err := os.WriteFile(pubPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err = pipeline.NewRunner(work, manifest, "repair-batch", testRange()).Run()
	if err != nil {
		t.Fatal(err)
	}
	if rep.PublishWrites != 1 || rep.RegeneratedArtifacts != 1 {
		t.Fatalf("hash repair = writes:%d regenerated:%d", rep.PublishWrites, rep.RegeneratedArtifacts)
	}
	got, err := pipeline.HashFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := pipeline.NewStore(filepath.Join(work, "state", "pipeline-state.json")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Artifacts["publish"].SHA256 != got || !st.Published {
		t.Fatal("repaired product hash was not committed")
	}
}
