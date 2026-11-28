package stage_test

import (
	"os"
	"path/filepath"
	"testing"

	"rtptrace/internal/fault"
	"rtptrace/internal/stage"
)

func fakeClock(ms *int64) func() int64 {
	return func() int64 { *ms++; return *ms }
}

func TestStateLifecycleAndAtomicCommit(t *testing.T) {
	var ms int64
	dir := t.TempDir()
	store, err := stage.OpenStore(dir, fakeClock(&ms))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st, err := store.Ensure("digest-1", 17, stage.OrderedNames)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := st.Stages[stage.NameScan].Status; got != stage.StatusPending {
		t.Fatalf("initial status=%s want pending", got)
	}
	if err := store.MarkRunning(st, stage.NameScan); err != nil {
		t.Fatalf("running: %v", err)
	}
	if st.Stages[stage.NameScan].Status != stage.StatusRunning || st.Stages[stage.NameScan].Attempts != 1 {
		t.Fatal("running transition failed")
	}
	digest, err := store.CommitArtifact(st, stage.NameScan, "artifacts/scan.json", []byte(`{}`))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	rec := st.Stages[stage.NameScan]
	if rec.Status != stage.StatusDone || rec.SHA256 != digest {
		t.Fatalf("done transition failed: %+v", rec)
	}
	// No temp file left behind and artifact exists at its final path.
	if _, err := os.Stat(filepath.Join(dir, "artifacts/scan.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp artifact must be removed after commit")
	}
	// State file itself is valid JSON persisted atomically.
	if _, err := os.Stat(filepath.Join(dir, "state.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp state file must be removed")
	}
}

func TestRestartReusesVerifiedStage(t *testing.T) {
	var ms int64
	dir := t.TempDir()
	execCount := map[string]int{}
	execs := map[string]stage.Executor{
		stage.NameScan: func(*stage.RunContext) (string, []byte, error) {
			execCount[stage.NameScan]++
			return "a.json", []byte("A"), nil
		},
		stage.NameRTP: func(*stage.RunContext) (string, []byte, error) {
			execCount[stage.NameRTP]++
			return "b.json", []byte("BB"), nil
		},
		stage.NameMerge: func(*stage.RunContext) (string, []byte, error) {
			execCount[stage.NameMerge]++
			return "c.json", []byte("CCC"), nil
		},
	}

	run := func(interrupt string) (*stage.Result, *stage.State, error) {
		store, _ := stage.OpenStore(dir, fakeClock(&ms))
		st, err := store.Ensure("digest-1", 17, stage.OrderedNames)
		if err != nil {
			return nil, nil, err
		}
		r := stage.NewRunner(store, execs)
		r.InterruptAfter = interrupt
		res, err := r.Run(st)
		return res, st, err
	}

	res, _, err := run(stage.NameRTP)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !res.Interrupted || res.InterruptedName != stage.NameRTP {
		t.Fatalf("expected interruption at rtp, got %+v", res)
	}
	// scan committed, rtp left running.
	store, _ := stage.OpenStore(dir, fakeClock(&ms))
	st, _ := store.Load()
	if st.Stages[stage.NameScan].Status != stage.StatusDone {
		t.Fatal("scan should be done after interrupt")
	}
	if st.Stages[stage.NameRTP].Status != stage.StatusRunning {
		t.Fatalf("rtp should be left running, got %s", st.Stages[stage.NameRTP].Status)
	}

	res2, _, err := run("")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res2.Recovered || res2.ResumedAt != stage.NameRTP {
		t.Fatalf("resume metadata wrong: %+v", res2)
	}
	if execCount[stage.NameScan] != 1 {
		t.Fatalf("scan re-executed %d times; checkpoint should be reused", execCount[stage.NameScan])
	}
	if execCount[stage.NameRTP] != 1 || execCount[stage.NameMerge] != 1 {
		t.Fatalf("resume should run rtp+merge once each: %v", execCount)
	}
}

func TestChecksumDriftForcesRedo(t *testing.T) {
	var ms int64
	dir := t.TempDir()
	var runs int
	execs := map[string]stage.Executor{
		stage.NameScan: func(*stage.RunContext) (string, []byte, error) {
			runs++
			return "a.json", []byte("A"), nil
		},
		stage.NameRTP:   func(*stage.RunContext) (string, []byte, error) { return "b.json", []byte("BB"), nil },
		stage.NameMerge: func(*stage.RunContext) (string, []byte, error) { return "c.json", []byte("CCC"), nil },
	}
	setup := func() *stage.Store {
		store, _ := stage.OpenStore(dir, fakeClock(&ms))
		st, _ := store.Ensure("d", 1, stage.OrderedNames)
		if err := store.MarkRunning(st, stage.NameScan); err != nil {
			t.Fatalf("running: %v", err)
		}
		if _, err := store.CommitArtifact(st, stage.NameScan, "a.json", []byte("A")); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return store
	}
	setup()
	// Corrupt the artifact behind the store's back.
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte("TAMPERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := stage.OpenStore(dir, fakeClock(&ms))
	st, _ := store.Load()
	err := store.VerifyDone(st.Stages[stage.NameScan])
	if err == nil || !fault.IsCorrupt(err) {
		t.Fatalf("expected corrupt fault on drift, got %v", err)
	}
	r := stage.NewRunner(store, execs)
	if _, err := r.Run(st); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if runs != 1 {
		t.Fatalf("damaged stage must be redone once, ran %d", runs)
	}
}

func TestFailedStageRecorded(t *testing.T) {
	var ms int64
	dir := t.TempDir()
	boom := fault.New(fault.KindInternal, "rtp", fault.NoOffset, "boom")
	execs := map[string]stage.Executor{
		stage.NameScan:  func(*stage.RunContext) (string, []byte, error) { return "a.json", []byte("A"), nil },
		stage.NameRTP:   func(*stage.RunContext) (string, []byte, error) { return "", nil, boom },
		stage.NameMerge: func(*stage.RunContext) (string, []byte, error) { return "c.json", []byte("C"), nil },
	}
	store, _ := stage.OpenStore(dir, fakeClock(&ms))
	st, _ := store.Ensure("d", 1, stage.OrderedNames)
	r := stage.NewRunner(store, execs)
	if _, err := r.Run(st); err == nil {
		t.Fatal("expected executor error to propagate")
	}
	if st.Stages[stage.NameRTP].Status != stage.StatusFailed || st.Stages[stage.NameRTP].Error == "" {
		t.Fatalf("failure not recorded: %+v", st.Stages[stage.NameRTP])
	}
}
