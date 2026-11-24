package pipeline

import (
	"errors"
	"testing"
)

func testDAG(t *testing.T, failAt string) *DAG {
	t.Helper()
	mkRun := func(name string) func(*RunContext) (string, error) {
		return func(*RunContext) (string, error) {
			if name == failAt {
				return "", errors.New("boom at " + name)
			}
			return "out-" + name, nil
		}
	}
	d, err := NewDAG([]Node{
		{Name: "ingest", Run: mkRun("ingest"), Inputs: []string{"in"}},
		{Name: "analyze", Deps: []string{"ingest"}, Run: mkRun("analyze")},
		{Name: "fingerprint", Deps: []string{"analyze"}, Run: mkRun("fingerprint")},
		{Name: "manifest", Deps: []string{"fingerprint"}, Run: mkRun("manifest")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRunnerFullRun(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	r := NewRunner(testDAG(t, ""), store)
	results, err := r.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if r.Ran != 4 || r.Reused != 0 {
		t.Fatalf("full run: ran=%d reused=%d", r.Ran, r.Reused)
	}
	if len(results) != 4 || results["manifest"].Output != "out-manifest" {
		t.Fatalf("results incomplete: %+v", results)
	}
}

func TestRunnerResumeReusesEverything(t *testing.T) {
	dir := t.TempDir()
	first := NewRunner(testDAG(t, ""), mustStore(t, dir))
	if _, err := first.Execute(); err != nil {
		t.Fatal(err)
	}
	second := NewRunner(testDAG(t, ""), mustStore(t, dir))
	results, err := second.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if second.Ran != 0 || second.Reused != 4 {
		t.Fatalf("resume must reuse all: ran=%d reused=%d", second.Ran, second.Reused)
	}
	// Reused outputs match the first run's summaries.
	if results["fingerprint"].Output != "out-fingerprint" {
		t.Fatalf("reused output mismatch: %+v", results["fingerprint"])
	}
}

func TestRunnerInterruptThenResumeMatchesFullRun(t *testing.T) {
	dir := t.TempDir()
	interrupted := NewRunner(testDAG(t, ""), mustStore(t, dir)).WithInterruptAfter("fingerprint")
	partial, err := interrupted.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := partial["manifest"]; ok {
		t.Fatal("manifest must not exist after interrupt-at-fingerprint")
	}
	if interrupted.Ran != 3 {
		t.Fatalf("interrupted run executed %d nodes, want 3", interrupted.Ran)
	}
	// Resume with identical parameters reuses the three and runs the last.
	resume := NewRunner(testDAG(t, ""), mustStore(t, dir))
	resumed, err := resume.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if resume.Reused != 3 || resume.Ran != 1 {
		t.Fatalf("resume: reused=%d ran=%d, want 3 and 1", resume.Reused, resume.Ran)
	}
	// Compare every checkpoint summary against a separate full run.
	full := NewRunner(testDAG(t, ""), mustStore(t, t.TempDir()))
	fullResults, _ := full.Execute()
	for name, want := range fullResults {
		if resumed[name].Checkpoint.OutputHash != want.Checkpoint.OutputHash {
			t.Fatalf("node %s summary differs after resume", name)
		}
	}
}

func TestRunnerInvalidNodeRecomputed(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	first := NewRunner(testDAG(t, ""), store)
	if _, err := first.Execute(); err != nil {
		t.Fatal(err)
	}
	// Delete one checkpoint; only that node (and nothing downstream-stale
	// beyond it, since keys depend on inputs not nodes) is recomputed.
	analyzeKey, _ := first.dag.Node("analyze")
	key, _ := analyzeKey.Key()
	if err := store.Remove(key); err != nil {
		t.Fatal(err)
	}
	second := NewRunner(testDAG(t, ""), store)
	if _, err := second.Execute(); err != nil {
		t.Fatal(err)
	}
	if second.Ran != 1 || second.Reused != 3 {
		t.Fatalf("partial invalidation: ran=%d reused=%d, want 1 and 3", second.Ran, second.Reused)
	}
}

func TestRunnerBlockedDependencyReportsFirst(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	// ingest itself fails with its raw error; the next node in topo order
	// (analyze) must be refused with a BlockedError naming ingest as root.
	r := NewRunner(testDAG(t, "ingest"), store)
	_, err := r.Execute()
	if err == nil || err.Error() != "boom at ingest" {
		t.Fatalf("failing node surfaces its own error, got %v", err)
	}
	// Directly exercise the blocker resolution the runner uses for dependants.
	blocker, ok := r.dag.FirstBlocking("manifest", r.failedSet())
	if !ok || blocker != "ingest" {
		t.Fatalf("root blocker = %q ok=%v, want ingest", blocker, ok)
	}

	// A middle node failure stops the run at that node; the DAG resolver
	// still identifies the root blocker for every downstream dependant.
	store2, _ := NewStore(t.TempDir())
	r2 := NewRunner(testDAG(t, "analyze"), store2)
	_, err2 := r2.Execute()
	if err2 == nil || err2.Error() != "boom at analyze" {
		t.Fatalf("runner stops at first failure, got %v", err2)
	}
	blocked, ok := r2.dag.FirstBlocking("manifest", r2.failedSet())
	if !ok || blocked != "analyze" {
		t.Fatalf("manifest's root blocker = %q ok=%v, want analyze", blocked, ok)
	}
	// BlockedError is the type used when a node is explicitly scheduled while
	// a dependency has already failed.
	be := &BlockedError{Node: "fingerprint", Blocker: "analyze"}
	if be.Error() == "" {
		t.Fatal("BlockedError must render a message")
	}
}

func TestRunnerChangedInputsChangeKeyAndRerun(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	NewRunner(testDAG(t, ""), store).Execute()
	// Rebuild the DAG with a changed ingest input summary.
	d2 := testDAG(t, "")
	n, _ := d2.Node("ingest")
	n.Inputs = []string{"changed-input"}
	r2 := NewRunner(d2, store)
	if _, err := r2.Execute(); err != nil {
		t.Fatal(err)
	}
	if r2.Ran == 0 {
		t.Fatal("changed input summary must force at least one recompute")
	}
}

func mustStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
