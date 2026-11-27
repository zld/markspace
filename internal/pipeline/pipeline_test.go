package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"gamepack/internal/fixture"
	"gamepack/internal/manifest"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

func baseConfig(root string) Config {
	return Config{
		Root: root,
		Codecs: []stream.Codec{stream.CodecH264, stream.CodecAV1},
		Bitrates: []int{500, 1000},
		Units: 12, KeyEvery: 4, WrapBits: 16,
		Inject: map[fixture.Injection]bool{
			fixture.InjectSwitch: true,
			fixture.InjectWrap:   true,
		},
	}
}

func TestFullRunProducesVerifiedManifest(t *testing.T) {
	cfg := baseConfig(t.TempDir())
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r.Run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.EntryCount == 0 {
		t.Fatal("manifest must contain entries")
	}
	if rep.ManifestSHA == "" {
		t.Fatal("manifest digest must be populated")
	}
	if rep.ZeroTickDiffs != rep.EntryCount {
		t.Fatalf("zero-tick cuts %d != entries %d", rep.ZeroTickDiffs, rep.EntryCount)
	}
	// The on-disk manifest must verify cleanly.
	body, err := os.ReadFile(rep.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Unmarshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(m); err != nil {
		t.Fatalf("manifest verify: %v", err)
	}
}

func TestCrashThenResumeCompletes(t *testing.T) {
	root := t.TempDir()
	cfg := baseConfig(root)
	cfg.CrashAfter = txn.StageAlign

	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(); !IsSimulatedCrash(err) {
		t.Fatalf("expected simulated crash, got %v", err)
	}
	// No manifest may be published before commit.
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest must not exist after an align-stage crash")
	}

	// A fresh runner over the same root resumes and finishes.
	resumeCfg := cfg
	resumeCfg.CrashAfter = ""
	second, err := New(resumeCfg)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := second.Run()
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if rep.ResumeStage == "" {
		t.Fatal("resume report must name the resume stage")
	}
	if _, err := os.Stat(rep.ManifestPath); err != nil {
		t.Fatalf("committed manifest missing: %v", err)
	}
}

func TestDeterministicRerunReusesAllEntries(t *testing.T) {
	root := t.TempDir()
	cfg := baseConfig(root)

	first, _ := New(cfg)
	r1, err := first.Run()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := New(cfg)
	r2, err := second.Run()
	if err != nil {
		t.Fatal(err)
	}
	if r1.ManifestSHA != r2.ManifestSHA {
		t.Fatal("identical inputs must yield identical manifest digest")
	}
	if r2.Diff.NewCount != 0 {
		t.Fatalf("rerun new entries = %d, want 0", r2.Diff.NewCount)
	}
	if r2.Diff.ReusedCount != r2.EntryCount {
		t.Fatalf("reused = %d, want %d", r2.Diff.ReusedCount, r2.EntryCount)
	}
	if r2.Diff.ChangedCount != 0 {
		t.Fatalf("changed = %d, want 0", r2.Diff.ChangedCount)
	}
}

func TestInputChangeReportsDiffAndNewDigest(t *testing.T) {
	root := t.TempDir()
	cfg := baseConfig(root)
	first, _ := New(cfg)
	r1, err := first.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Change the input shape (more units), forcing a different input digest.
	cfg2 := cfg
	cfg2.Units = 16
	second, _ := New(cfg2)
	r2, err := second.Run()
	if err != nil {
		t.Fatal(err)
	}
	if r2.ManifestSHA == r1.ManifestSHA {
		t.Fatal("changed input must change the manifest digest")
	}
	if !r2.Diff.InputChanged {
		t.Fatal("input change must be flagged")
	}
	if r2.Diff.ReusedCount != 0 {
		t.Fatalf("no entry may be reused after input change, got %d", r2.Diff.ReusedCount)
	}
}
