package resume_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/resume"
)

func testPipeline(t *testing.T, dir string) resume.Pipeline {
	t.Helper()
	m := &manifest.Manifest{SchemaVersion: manifest.CanonicalVersion}
	cfg := &manifest.ConfigPackage{SchemaVersion: manifest.CanonicalVersion, Seed: 17}
	return resume.Pipeline{
		Dir: dir, JobID: "test-job", Manifest: m, Config: cfg,
		Clusters: []resume.Cluster{
			{Index: 0, Input: []byte("cluster-zero-input"), Kind: "test"},
			{Index: 1, Input: []byte("cluster-one-input"), Kind: "test"},
			{Index: 2, Input: []byte("cluster-two-input"), Kind: "test"},
		},
		InterruptAfter: -1,
	}
}

func TestInterruptResumeReplayAndCleanOutputsMatch(t *testing.T) {
	dir := t.TempDir()
	p := testPipeline(t, dir)
	p.InterruptAfter = 0
	partial, err := p.Run()
	if !errors.Is(err, resume.ErrSimulatedInterrupt) {
		t.Fatalf("interrupt err=%v", err)
	}
	if len(partial.Processed) != 1 {
		t.Fatalf("processed=%v", partial.Processed)
	}

	p.InterruptAfter = -1
	resumed, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Skipped) != 1 || resumed.Skipped[0] != 0 {
		t.Fatalf("only cluster zero should be skipped: %+v", resumed)
	}
	if len(resumed.Processed) != 2 {
		t.Fatalf("processed=%v", resumed.Processed)
	}
	if len(resumed.OutputSHA256) != 64 {
		t.Fatalf("bad output sha %q", resumed.OutputSHA256)
	}

	replay, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Skipped) != 3 || replay.OutputSHA256 != resumed.OutputSHA256 {
		t.Fatalf("replay skipped=%v hash match=%v", replay.Skipped, replay.OutputSHA256 == resumed.OutputSHA256)
	}

	clean := testPipeline(t, filepath.Join(t.TempDir(), "clean"))
	cleanResult, err := clean.Run()
	if err != nil {
		t.Fatal(err)
	}
	if cleanResult.OutputSHA256 != resumed.OutputSHA256 {
		t.Fatalf("clean hash=%s resumed=%s", cleanResult.OutputSHA256, resumed.OutputSHA256)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 4 && e.Name()[:4] == ".tmp" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestCheckpointBindingDetectsConfigAndInputChanges(t *testing.T) {
	dir := t.TempDir()
	p := testPipeline(t, dir)
	old, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	oldOutput, err := resume.SHA256File(old.OutputPath)
	if err != nil {
		t.Fatal(err)
	}

	changedCfg := *p.Config
	changedCfg.Seed = 999
	changed := p
	changed.Config = &changedCfg
	_, err = changed.Run()
	if !media.IsConflict(err) || !errors.Is(err, media.ErrConfigChanged) {
		t.Fatalf("config conflict err=%v", err)
	}
	newOutput, _ := resume.SHA256File(old.OutputPath)
	if newOutput != oldOutput {
		t.Fatal("output changed after config conflict")
	}

	inputChanged := p
	inputChanged.Clusters = append([]resume.Cluster(nil), p.Clusters...)
	inputChanged.Clusters[0].Input = []byte("tampered-prefix")
	_, err = inputChanged.Run()
	if !errors.Is(err, media.ErrInputChanged) {
		t.Fatalf("input conflict err=%v", err)
	}
}

func TestVerifyBindingManifestVersion(t *testing.T) {
	cp := &resume.Checkpoint{Binding: resume.Binding{
		InputPrefixSHA256: "abc", InputPrefixBytes: 3,
		ManifestVersion: "2.0.0", ConfigSHA256: "cfg",
	}}
	err := cp.VerifyBinding(resume.Binding{
		InputPrefixSHA256: "abc", InputPrefixBytes: 3,
		ManifestVersion: manifest.CanonicalVersion, ConfigSHA256: "cfg",
	})
	if !errors.Is(err, media.ErrManifestChanged) {
		t.Fatalf("err=%v", err)
	}
}
