package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func baseCfg(t *testing.T, runID, out string) Config {
	t.Helper()
	return Config{
		RunID: runID, Seed: 7, WorkDir: t.TempDir(), OutPath: out,
		JitterWindow: 8, SyncToleranceTicks: 1 << 20,
	}
}

func TestFullRunProducesReportAndFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.mp4")
	e, err := NewEngine(baseCfg(t, "run-full", out))
	if err != nil {
		t.Fatal(err)
	}
	rep := e.Run()
	if rep.FirstError != nil {
		t.Fatalf("unexpected error: %v", rep.FirstError)
	}
	if rep.Packets != demoPackets {
		t.Fatalf("packets = %d, want %d", rep.Packets, demoPackets)
	}
	if rep.Summary == "" || rep.ProvenanceDigest == "" {
		t.Fatalf("missing digests: %+v", rep)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("output file missing/empty: %v", err)
	}
	// Every node must be recorded in provenance.
	for _, n := range []string{NodeParse, NodeJitter, NodeRecover, NodeSync, NodeMux} {
		found := false
		for _, en := range e.Recorder().Entries() {
			if en.Node == n {
				found = true
			}
		}
		if !found {
			t.Fatalf("provenance missing node %s", n)
		}
	}
}

func TestRepeatedRunsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mp4")
	cfg := baseCfg(t, "run-det", out)
	e1, _ := NewEngine(cfg)
	r1 := e1.Run()

	// A fresh engine reuses every durable node and rewrites the same output
	// path; the bytes (and thus digest) must be identical.
	cfg2 := baseCfg(t, "run-det", out)
	cfg2.WorkDir = cfg.WorkDir
	e2, _ := NewEngine(cfg2)
	r2 := e2.Run()

	if r1.Summary != r2.Summary {
		t.Fatalf("output digest changed across runs: %s vs %s", r1.Summary, r2.Summary)
	}
	if r1.ProvenanceDigest != r2.ProvenanceDigest {
		t.Fatal("provenance digest must be stable")
	}
	if len(r2.ReusedNodes) != 5 {
		t.Fatalf("reused nodes = %v, want all 5", r2.ReusedNodes)
	}
}

func TestInterruptResumesFromLastCompleteNode(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "resume.mp4")

	// First engine fails once inside the recover node before its commit.
	cfg1 := baseCfg(t, "run-resume", out)
	cfg1.FailNodeOnce = NodeRecover
	e1, _ := NewEngine(cfg1)
	rep1 := e1.Run()
	if rep1.FirstError == nil || rep1.BlockedDependency == nil {
		t.Fatalf("expected interruption, got %+v", rep1)
	}
	// recover failed, so the first node it blocks is sync.
	if rep1.BlockedDependency.Node != NodeSync || rep1.BlockedDependency.Dependency != NodeRecover {
		t.Fatalf("blocked = %+v", rep1.BlockedDependency)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("mux output must not exist when an upstream node failed")
	}
	// Upstream nodes that committed before the failure must be durable.
	for _, n := range []string{NodeParse, NodeJitter} {
		if st, ok := e1.Store().Get(n); !ok || st.Status != "done" {
			t.Fatalf("node %s should be durably complete", n)
		}
	}

	// Second engine with the same workdir resumes; recover reruns, later nodes run.
	cfg2 := baseCfg(t, "run-resume", out)
	cfg2.WorkDir = cfg1.WorkDir // share durable checkpoint and artifacts
	e2, _ := NewEngine(cfg2)
	rep2 := e2.Run()
	if rep2.FirstError != nil {
		t.Fatalf("resume must succeed, got %v", rep2.FirstError)
	}
	// parse and jitter were durably complete and must be reused.
	reused := map[string]bool{}
	for _, n := range rep2.ReusedNodes {
		reused[n] = true
	}
	if !reused[NodeParse] || !reused[NodeJitter] {
		t.Fatalf("expected parse+jitter reuse, got %v", rep2.ReusedNodes)
	}
	if reused[NodeRecover] {
		t.Fatal("the interrupted recover node must rerun, not be reused")
	}
	if rep2.Summary == "" {
		t.Fatal("resumed run must produce an output digest")
	}
}

func TestSyncToleranceBlocksMux(t *testing.T) {
	out := filepath.Join(t.TempDir(), "blocked.mp4")
	cfg := baseCfg(t, "run-sync", out)
	cfg.SyncToleranceTicks = 1 // any non-zero offset now exceeds tolerance
	cfg.VideoDelayTicks = 500
	e, _ := NewEngine(cfg)
	rep := e.Run()
	if rep.FirstError == nil {
		t.Fatal("expected a sync failure")
	}
	if rep.FirstError.Category.String() != "sync" {
		t.Fatalf("category = %s, want sync", rep.FirstError.Category)
	}
	if rep.SyncOffsetTicks == 0 {
		t.Fatal("report must carry the measured offset")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("mux must be blocked: no output file allowed")
	}
}

func TestFirstErrorOffsetHelper(t *testing.T) {
	if got := firstErrorOffset(nil); got != -1 {
		t.Fatalf("nil offset = %d, want -1", got)
	}
}
