package batch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/batch"
	"mediapack/internal/clock"
	"mediapack/internal/fixture"
	"mediapack/internal/model"
)

func generate(t *testing.T, dir string, inject ...string) {
	t.Helper()
	cfg := fixture.Config{
		Dir: filepath.Join(dir, "input"),
		Tracks: 2, Fragments: 16, Inject: inject,
	}
	if err := fixture.Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

func run(t *testing.T, dir string) *batch.Report {
	t.Helper()
	rep, err := batch.Run(batch.Options{WorkDir: dir, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func TestRunWithFaults(t *testing.T) {
	dir := t.TempDir()
	generate(t, dir,
		fixture.FaultGap, fixture.FaultTfdtBackstep, fixture.FaultTruncatedBox,
		fixture.FaultMissingTfdt, fixture.FaultDupMfhd)
	rep := run(t, dir)
	if rep.Accepted != 16 {
		t.Errorf("accepted=%d want 16", rep.Accepted)
	}
	if rep.Rejected != 1 { // truncated box
		t.Errorf("rejected=%d want 1", rep.Rejected)
	}
	if rep.Quarantined != 1 { // duplicate mfhd with conflicting hash
		t.Errorf("quarantined=%d want 1", rep.Quarantined)
	}
	if rep.Repaired < 2 { // tfdt backstep + missing tfdt
		t.Errorf("repaired=%d want >=2", rep.Repaired)
	}
	if rep.Unrecoverable != 0 {
		t.Errorf("unrecoverable=%d want 0", rep.Unrecoverable)
	}
	if len(rep.Hashes) != 16 {
		t.Errorf("hashes=%d want 16", len(rep.Hashes))
	}
}

func TestRunResumeDeterministic(t *testing.T) {
	dir := t.TempDir()
	generate(t, dir, fixture.FaultGap)
	rep1 := run(t, dir)
	rep2 := run(t, dir)
	if rep2.ReusedChunks != 4 {
		t.Errorf("reused=%d want 4", rep2.ReusedChunks)
	}
	matched, total := batch.HashMatch(rep1, rep2)
	if matched != total || total != 16 {
		t.Errorf("hash match %d/%d", matched, total)
	}
}

func TestRunRecoversPartialIndex(t *testing.T) {
	clean := t.TempDir()
	generate(t, clean, fixture.FaultGap)
	want := run(t, clean)

	dir := t.TempDir()
	generate(t, dir, fixture.FaultGap)
	if err := fixture.PlantPartialIndex(dir); err != nil {
		t.Fatal(err)
	}
	rep := run(t, dir)
	if rep.RecoveredChunks != 1 {
		t.Errorf("recovered=%d want 1", rep.RecoveredChunks)
	}
	// Recovery must produce the same result as an uninterrupted run.
	matched, total := batch.HashMatch(want, rep)
	if matched != total {
		t.Errorf("recovered run diverges: %d/%d", matched, total)
	}
}

// writeUnrecoverableInput creates an input dir with one good and one
// unrecoverable fragment (no tfdt, no derivable duration).
func writeUnrecoverableInput(t *testing.T, dir string) {
	t.Helper()
	inDir := filepath.Join(dir, "input")
	trackDir := filepath.Join(inDir, "track-0001")
	if err := os.MkdirAll(trackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tracks := []model.TrackDesc{fixture.TrackDesc(1)}
	manifest, _ := json.Marshal(tracks)
	if err := os.WriteFile(filepath.Join(inDir, "tracks.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	good, err := fixture.BuildFragment(fixture.TrackDesc(1), fixture.FragOpts{
		Mfhd: 1, HasTfdt: true, Keyframe: true, Duration: 3000, Seed: []byte{9},
	})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := fixture.BuildFragment(fixture.TrackDesc(1), fixture.FragOpts{
		Mfhd: 2, HasTfdt: false, Duration: 0, Seed: []byte{8},
	})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(trackDir, "frag-000000.mp4"), good, 0o644)
	os.WriteFile(filepath.Join(trackDir, "frag-000001.mp4"), bad, 0o644)
}

func TestRunUnrecoverableFragment(t *testing.T) {
	dir := t.TempDir()
	writeUnrecoverableInput(t, dir)
	rep, err := batch.Run(batch.Options{WorkDir: dir, ChunkSize: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Unrecoverable != 1 || rep.Rejected != 1 || rep.Accepted != 1 {
		t.Errorf("unrecoverable=%d rejected=%d accepted=%d",
			rep.Unrecoverable, rep.Rejected, rep.Accepted)
	}
	// The unrecoverable fragment must not appear in the index.
	for _, e := range rep.Index.Entries() {
		if e.Mfhd == 2 {
			t.Error("unrecoverable fragment reached the index")
		}
	}
}

func TestRunRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	writeUnrecoverableInput(t, dir)
	target := filepath.Join(dir, "input", "tracks.json")
	link := filepath.Join(dir, "input", "track-0001", "evil.mp4")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	_, err := batch.Run(batch.Options{WorkDir: dir, ChunkSize: 4})
	if err == nil || !clock.HasCategory(err, clock.CatIO) {
		t.Fatalf("want io error for symlink, got %v", err)
	}
}

func TestRunCheckpointOnlyReferencesValidChunks(t *testing.T) {
	dir := t.TempDir()
	generate(t, dir)
	rep := run(t, dir)
	if err := batch.CheckInvariants(dir, rep); err != nil {
		t.Fatalf("invariants: %v", err)
	}
	if rep.Checkpoint == nil || !rep.Checkpoint.Complete {
		t.Fatal("checkpoint must be complete")
	}
	for _, rec := range rep.Checkpoint.Chunks {
		if !rec.Done {
			t.Errorf("chunk %d not done", rec.Index)
		}
	}
}
