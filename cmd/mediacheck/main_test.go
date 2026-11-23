package main

import (
	"testing"

	"mediacheck/internal/model"
	"mediacheck/internal/txn"
)

func TestParseFlagsAliases(t *testing.T) {
	opts, err := parseFlags([]string{"--seed", "17", "--faults", "truncate,late,metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.seed != 17 || len(opts.faults) != 3 {
		t.Fatalf("parsed opts wrong: %+v", opts)
	}
	want := map[model.FaultKind]bool{
		model.FaultTruncate: true, model.FaultLate: true, model.FaultMetaConflict: true,
	}
	for _, f := range opts.faults {
		if !want[f] {
			t.Fatalf("unexpected fault %s", f)
		}
	}
}

func TestParseFlagsRejectsUnknown(t *testing.T) {
	if _, err := parseFlags([]string{"--faults", "nope"}); err == nil {
		t.Fatal("unknown fault must error")
	}
	if _, err := parseFlags([]string{"--seed"}); err == nil {
		t.Fatal("missing flag value must error")
	}
}

func TestRunHealthyExitZero(t *testing.T) {
	dir := t.TempDir()
	code, err := execute(options{seed: 17, faults: nil}, dir)
	if err != nil || code != exitOK {
		t.Fatalf("healthy: code=%d err=%v", code, err)
	}
}

func TestRunDamagedExitTwo(t *testing.T) {
	dir := t.TempDir()
	code, err := execute(options{seed: 17, faults: []model.FaultKind{
		model.FaultTruncate, model.FaultLate, model.FaultMetaConflict,
	}}, dir)
	if err != nil || code != exitDamaged {
		t.Fatalf("damaged: code=%d err=%v", code, err)
	}
}

func TestResumeMatchesCleanHash(t *testing.T) {
	faults := []model.FaultKind{model.FaultTruncate, model.FaultLate}
	dir := t.TempDir()
	// Seed a WAL in the exact state a hard crash leaves behind: a durable
	// prepared record with no commit marker.
	j, err := txn.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	crashTxn := "seed-17-faults-truncated-late"
	if err := j.Append(&txn.Entry{
		TxnID: crashTxn, Stage: model.StageParse, State: txn.StatePrepared,
		Payload: map[string]interface{}{"seed": 17, "faults": "truncated-late"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	code, err := execute(options{seed: 17, resume: true, faults: faults}, dir)
	if err != nil || code != exitDamaged {
		t.Fatalf("resume: code=%d err=%v", code, err)
	}
	cleanDir := t.TempDir()
	cleanCode, err := execute(options{seed: 17, faults: faults}, cleanDir)
	if err != nil || cleanCode != exitDamaged {
		t.Fatalf("clean: code=%d err=%v", cleanCode, err)
	}
	if h1, h2 := payloadHash(t, dir), payloadHash(t, cleanDir); h1 == "" || h1 != h2 {
		t.Fatalf("resume hash %q != clean hash %q", h1, h2)
	}
}

// payloadHash opens the journal and returns the single committed record hash.
func payloadHash(t *testing.T, dir string) string {
	t.Helper()
	j, err := txn.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ids := j.CommittedIDs()
	if len(ids) != 1 {
		t.Fatalf("want exactly one committed txn, got %v", ids)
	}
	return j.ContentHash(ids[0])
}
