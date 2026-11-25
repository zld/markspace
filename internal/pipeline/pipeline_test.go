package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/txn"
)

func TestRunHappyPathCommitsAllStages(t *testing.T) {
	dir := t.TempDir()
	fx, err := fixture.Generate(7, fixture.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if err := PersistFixture(dir, fx); err != nil {
		t.Fatal(err)
	}
	p, err := New(DefaultConfig(7), dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Run(fx)
	if err != nil {
		t.Fatalf("happy path must succeed: %v", err)
	}
	p.Close()

	if len(res.Stages) != len(txn.Stages) {
		t.Fatalf("committed stages = %d, want %d", len(res.Stages), len(txn.Stages))
	}
	if res.SummaryHash == "" || res.FailedStage != "" {
		t.Fatalf("result incomplete: %+v", res)
	}
	// Every stage has a manifest with at least one hashed artifact.
	mgr, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range txn.Stages {
		if err := mgr.VerifyManifest(st); err != nil {
			t.Fatalf("manifest %s not verifiable: %v", st, err)
		}
	}
	mgr.Close()
	// No staging leftovers after a clean commit run.
	if _, err := os.ReadDir(filepath.Join(dir, "staging")); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "staging")); len(entries) != 0 {
		t.Fatalf("staging must be empty, got %v", entries)
	}
}

func TestRunTruncatedFailsAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	fx, err := fixture.Generate(7, fixture.FaultTruncated)
	if err != nil {
		t.Fatal(err)
	}
	if err := PersistFixture(dir, fx); err != nil {
		t.Fatal(err)
	}
	p, _ := New(DefaultConfig(7), dir)
	res, err := p.Run(fx)
	if err == nil {
		t.Fatal("truncated input must fail")
	}
	p.Close()

	if res.FailedStage != txn.StageMetadata || res.Failure == nil {
		t.Fatalf("want metadata failure with located fault, got stage=%q failure=%+v",
			res.FailedStage, res.Failure)
	}
	if res.Failure.Code != "TRUNCATED_HEADER" || res.Failure.Offset != fx.TruncatedOffset {
		t.Fatalf("fault not located at truncation offset: %+v", res.Failure)
	}
	// No committed stages and no half-finished artifacts.
	if _, err := os.Stat(filepath.Join(dir, "committed", txn.StageMetadata)); !os.IsNotExist(err) {
		t.Fatal("failed metadata stage must leave no committed directory")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "staging")); len(entries) != 0 {
		t.Fatalf("rollback must clear staging, got %v", entries)
	}
	// The full fixture is still on disk for recovery.
	if _, err := LoadFixture(dir); err != nil {
		t.Fatalf("fixture must survive for recovery: %v", err)
	}
}

func TestRecoverThenResumeProducesStableHash(t *testing.T) {
	dir := t.TempDir()
	fx, _ := fixture.Generate(7, fixture.FaultTruncated)
	if err := PersistFixture(dir, fx); err != nil {
		t.Fatal(err)
	}
	p, _ := New(DefaultConfig(7), dir)
	if _, err := p.Run(fx); err == nil {
		t.Fatal("truncated run should fail first")
	}
	p.Close()

	// Recover must clean the uncommitted metadata stage and report nothing done.
	done, err := Recover(dir)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("no stage should be committed, got %v", done)
	}

	// Resume with the full (untruncated) fixture.
	full, _ := fixture.Generate(7, fixture.FaultNone)
	p2, _ := New(DefaultConfig(7), dir)
	res, err := p2.Run(full)
	if err != nil {
		t.Fatalf("resumed run must succeed: %v", err)
	}
	p2.Close()
	if len(res.Stages) != len(txn.Stages) {
		t.Fatalf("resumed run committed %d stages", len(res.Stages))
	}
}

func TestVerifyRecomputesAndMatches(t *testing.T) {
	dir := t.TempDir()
	fx, _ := fixture.Generate(7, fixture.FaultNone)
	PersistFixture(dir, fx)
	p, _ := New(DefaultConfig(7), dir)
	if _, err := p.Run(fx); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := Verify(dir, 1); err != nil {
		t.Fatalf("verify must pass with 1 ms tolerance: %v", err)
	}
}

func TestRunsAreDeterministic(t *testing.T) {
	run := func() string {
		dir := t.TempDir()
		fx, _ := fixture.Generate(11, fixture.FaultNone)
		PersistFixture(dir, fx)
		p, _ := New(DefaultConfig(11), dir)
		res, err := p.Run(fx)
		if err != nil {
			t.Fatal(err)
		}
		p.Close()
		return res.SummaryHash
	}
	if run() != run() {
		t.Fatal("identical inputs must yield identical summary hashes")
	}
}

func TestSeedChangesSummaryHash(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	fxA, _ := fixture.Generate(1, fixture.FaultNone)
	fxB, _ := fixture.Generate(2, fixture.FaultNone)
	PersistFixture(dirA, fxA)
	PersistFixture(dirB, fxB)
	pA, _ := New(DefaultConfig(1), dirA)
	rA, _ := pA.Run(fxA)
	pA.Close()
	pB, _ := New(DefaultConfig(2), dirB)
	rB, _ := pB.Run(fxB)
	pB.Close()
	if rA.SummaryHash == rB.SummaryHash {
		t.Fatal("different seeds must produce different summary hashes")
	}
}
