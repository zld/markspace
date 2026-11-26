package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/fixture"
	"continuity/internal/index"
)

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// genOK builds a fresh ok fixture tree and returns its root.
func genOK(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "input")
	g, err := fixture.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunHappyPath(t *testing.T) {
	input := genOK(t)
	work := t.TempDir()
	r, err := NewRunner(Config{
		InputDir: input,
		IndexDir: filepath.Join(work, "index"),
		OutputDir: filepath.Join(work, "out"),
		Chunk:    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Recovered != 1 { // one shared program interval recovered
		t.Errorf("recovered = %d, want 1", rep.Recovered)
	}
	if rep.DuplicatePublish != 0 {
		t.Errorf("duplicate publish = %d", rep.DuplicatePublish)
	}
	if rep.ManifestSHA256 == "" {
		t.Error("manifest hash empty")
	}
	if len(rep.Representations) != 3 {
		t.Fatalf("reports = %d", len(rep.Representations))
	}
	for _, row := range rep.Representations {
		if row.DeviationPct > 5.0 {
			t.Errorf("%s deviation %.2f%% exceeds 5%%", row.RepID, row.DeviationPct)
		}
		if row.PSNRDB < 35 {
			t.Errorf("%s PSNR %.2f below 35 dB", row.RepID, row.PSNRDB)
		}
		if row.MaxAlignMS > 1.0 {
			t.Errorf("%s align %.3f ms exceeds 1 ms", row.RepID, row.MaxAlignMS)
		}
	}
}

func TestCrashThenResumeRecovers(t *testing.T) {
	input := genOK(t)
	work := t.TempDir()
	indexDir := filepath.Join(work, "index")
	outDir := filepath.Join(work, "out")

	first, err := NewRunner(Config{
		InputDir: input, IndexDir: indexDir, OutputDir: outDir,
		Chunk: 2, CrashAfterWrite: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Run()
	if _, ok := err.(*index.InjectedCrash); !ok {
		t.Fatalf("want injected crash, got %v", err)
	}
	// No published manifest must exist after the crash.
	if exists(filepath.Join(outDir, "published.json")) {
		t.Fatal("incomplete manifest was published after crash")
	}

	// Resume: orphaned store file is reconciled and the run completes.
	second, err := NewRunner(Config{
		InputDir: input, IndexDir: indexDir, OutputDir: outDir,
		Chunk: 2, Resume: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := second.Run()
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if rep.Recovered < 1 {
		t.Errorf("recovered = %d", rep.Recovered)
	}
	if rep.DuplicatePublish != 0 {
		t.Errorf("duplicate publish after resume = %d", rep.DuplicatePublish)
	}

	// Repeated verify is deterministic: identical manifest hash.
	v1, err := second.Verify()
	if err != nil {
		t.Fatal(err)
	}
	v2, err := second.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if v1.ManifestSHA256 != v2.ManifestSHA256 || v1.ManifestSHA256 == "" {
		t.Fatalf("manifest hash unstable: %q vs %q",
			v1.ManifestSHA256, v2.ManifestSHA256)
	}
}

func TestTruncatedMdatRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "input")
	g, err := fixture.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.GenerateBad(fixture.CaseTruncatedMdat); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	r, err := NewRunner(Config{
		InputDir: root, IndexDir: filepath.Join(work, "idx"),
		OutputDir: filepath.Join(work, "out"), Chunk: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err == nil {
		t.Fatal("expected corrupt media error")
	}
	// Nothing published on failure.
	if exists(filepath.Join(work, "out", "published.json")) {
		t.Fatal("incomplete manifest published after corrupt input")
	}
}
