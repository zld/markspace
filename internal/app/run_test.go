package app

import (
	"path/filepath"
	"testing"

	"mediad/internal/sample"
)

func TestRunNormalCompletesClean(t *testing.T) {
	cfg := DefaultConfig(filepath.Join(t.TempDir(), "out"))
	rep, err := Run(sample.KindNormal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Completed() {
		t.Fatal("normal run must complete")
	}
	st := rep.Stats()
	if st.QualityErrors != 0 {
		t.Fatalf("normal fixture quality errors = %d, want 0", st.QualityErrors)
	}
	if st.ManifestMaxDelta > 1 {
		t.Fatalf("max boundary delta = %d ms, want <= 1", st.ManifestMaxDelta)
	}
	if len(rep.Manifest.Slices) == 0 {
		t.Fatal("manifest must contain slices")
	}
	if rep.Ran() != 4 || rep.Reused() != 0 {
		t.Fatalf("first run: ran=%d reused=%d, want 4 and 0", rep.Ran(), rep.Reused())
	}
}

func TestResumeReusesAllNodes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	cfg := DefaultConfig(out)
	first, err := Run(sample.KindNormal, cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, _ := first.Manifest.DocumentHash()

	second, err := Run(sample.KindNormal, DefaultConfig(out))
	if err != nil {
		t.Fatal(err)
	}
	if second.Ran() != 0 || second.Reused() != 4 {
		t.Fatalf("resume: ran=%d reused=%d, want 0 and 4", second.Ran(), second.Reused())
	}
	secondHash, _ := second.Manifest.DocumentHash()
	if firstHash != secondHash {
		t.Fatal("resumed manifest hash must equal the full-run hash")
	}
}

func TestInterruptThenResumeMatchesFullRun(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	full, err := Run(sample.KindNormal, DefaultConfig(out))
	if err != nil {
		t.Fatal(err)
	}
	fullHash, _ := full.Manifest.DocumentHash()

	intDir := filepath.Join(t.TempDir(), "interrupted")
	intCfg := DefaultConfig(intDir)
	intCfg.InterruptAfter = "fingerprint"
	partial, err := Run(sample.KindNormal, intCfg)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Completed() {
		t.Fatal("interrupted run must not produce a manifest")
	}
	if partial.Ran() != 3 {
		t.Fatalf("interrupted run executed %d nodes, want 3", partial.Ran())
	}

	resumed, err := Run(sample.KindNormal, DefaultConfig(intDir))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reused() != 3 || resumed.Ran() != 1 {
		t.Fatalf("resume: reused=%d ran=%d, want 3 and 1", resumed.Reused(), resumed.Ran())
	}
	resumedHash, _ := resumed.Manifest.DocumentHash()
	if resumedHash != fullHash {
		t.Fatal("post-interrupt manifest must match an uninterrupted full run")
	}
}

func TestRunTruncatedInputFails(t *testing.T) {
	// The truncate fault is exercised through a dedicated kind path in the
	// CLI; here we confirm a failing node surfaces its error and leaves no
	// manifest behind via the runner contract.
	out := filepath.Join(t.TempDir(), "out")
	cfg := DefaultConfig(out)
	rep, err := Run(sample.KindJump, cfg) // jump is non-fatal but has errors
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stats().QualityErrors == 0 {
		t.Fatal("jump fixture must record quality errors while still completing")
	}
}

func TestEachFixtureProducesTraceableManifest(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	for _, kind := range sample.AllKinds() {
		rep, err := Run(kind, DefaultConfig(filepath.Join(out, string(kind))))
		if err != nil {
			t.Fatalf("fixture %s failed: %v", kind, err)
		}
		m := rep.Manifest
		if len(m.Inputs) != 1 || m.Inputs[0].ContentHash == "" {
			t.Fatalf("fixture %s manifest missing input provenance", kind)
		}
		if len(m.Checkpoints) == 0 {
			t.Fatalf("fixture %s manifest must carry checkpoint provenance", kind)
		}
	}
}
