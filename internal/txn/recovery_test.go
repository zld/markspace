package txn_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"assetcheck/internal/errmodel"
	"assetcheck/internal/fixture"
	"assetcheck/internal/index"
	"assetcheck/internal/stream"
	"assetcheck/internal/txn"
	"assetcheck/internal/wav"
)

func prepareGood(t *testing.T) (contentFP string, idx *index.Index) {
	t.Helper()
	data := fixture.NamedFixtures()["good.wav"]
	w, err := wav.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := stream.Scan(w, data, stream.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	contentFP = wav.ContentFingerprint(w)
	idx, err = index.Build(rep, contentFP, wav.MetadataFingerprint(data, w))
	if err != nil {
		t.Fatal(err)
	}
	return contentFP, idx
}

func commitOne(t *testing.T, dir string, seqWant uint64) (string, *index.Index) {
	t.Helper()
	contentFP, idx := prepareGood(t)
	store, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	txid, seq, err := mgr.Begin()
	if err != nil || seq != seqWant {
		t.Fatalf("begin seq=%d err=%v, want %d", seq, err, seqWant)
	}
	if err := mgr.Stage(txid, "scan", "cp-scan"); err != nil {
		t.Fatal(err)
	}
	a := store.NewArtifact(txid, contentFP, seq)
	if err := store.WriteStaging(&a, idx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordPrepared(a); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Commit(a, store); err != nil {
		t.Fatal(err)
	}
	return contentFP, idx
}

func TestInjectedFailureRollsBackAndRecommitsSameDigest(t *testing.T) {
	dir := t.TempDir()
	contentFP, idx1 := commitOne(t, dir, 1)

	// Second transaction: prepare then "crash" before the commit record.
	store, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	txid, seq, err := mgr.Begin()
	if err != nil || seq != 2 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
	a := store.NewArtifact(txid, contentFP, seq)
	if err := store.WriteStaging(&a, idx1); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordPrepared(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.StagingPath); err != nil {
		t.Fatalf("staging file should exist before crash: %v", err)
	}
	// Simulate the CLI injection point returning E5003.
	injected := errmodel.New(errmodel.ErrInjected, "injected after-index-prepare")
	if errmodel.ExitCode(injected) != 4 {
		t.Fatal("injected error must map to exit 4")
	}
	mgr.Close()

	// While crashed, manifest still exposes only seq 1.
	visible, e, err := store.Visible(contentFP)
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 1 || visible.Digest != idx1.Digest {
		t.Fatalf("half-committed index leaked: entry=%+v", e)
	}

	// Resume: recovery must roll back the prepared txn and drop staging.
	mgr2, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr2.Recover(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RolledBack) != 1 || res.LastCommitSeq != 1 {
		t.Fatalf("recovery=%+v", res)
	}
	if _, err := os.Stat(a.StagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging must be removed, stat err=%v", err)
	}
	mgr2.Close()

	// Re-run: candidate seq is 2 again and the committed digest is identical.
	_, idx2 := commitOne(t, dir, 2)
	if idx2.Digest != idx1.Digest {
		t.Fatal("resumed result digest differs from uninterrupted result")
	}
	store2, _ := index.OpenStore(dir)
	_, e2, err := store2.Visible(contentFP)
	if err != nil || e2.Seq != 2 {
		t.Fatalf("visible seq after resume = %+v err=%v", e2, err)
	}
}

func TestRecoveryIsIdempotentAcrossReopens(t *testing.T) {
	dir := t.TempDir()
	contentFP, idx := prepareGood(t)
	store, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ := txn.NewManager(dir)
	txid, seq, _ := mgr.Begin()
	a := store.NewArtifact(txid, contentFP, seq)
	if err := store.WriteStaging(&a, idx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordPrepared(a); err != nil {
		t.Fatal(err)
	}
	mgr.Close()

	openAndRecover := func() txn.RecoveryResult {
		m, err := txn.NewManager(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		res, err := m.Recover(store)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := openAndRecover()
	if len(first.RolledBack) != 1 {
		t.Fatalf("first recovery=%+v", first)
	}
	second := openAndRecover()
	if len(second.RolledBack) != 0 || len(second.RolledForward) != 0 {
		t.Fatalf("second recovery must be a no-op, got %+v", second)
	}
}

func TestRecoveryRollsForwardCommittedUnapplied(t *testing.T) {
	dir := t.TempDir()
	contentFP, idx := prepareGood(t)
	store, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	txid, seq, _ := mgr.Begin()
	a := store.NewArtifact(txid, contentFP, seq)
	if err := store.WriteStaging(&a, idx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordPrepared(a); err != nil {
		t.Fatal(err)
	}
	// Commit record durable, but process "dies" before Publish/Applied.
	if _, err := mgr.WAL().Append(txn.Record{
		Type: txn.RecCommit, TxnID: txid, Seq: seq, Key: a.Key,
		Staging: a.StagingPath, Final: a.FinalPath, SHA256: a.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	mgr.Close()
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manifest should not exist before publish")
	}

	mgr2, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr2.Recover(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RolledForward) != 1 || res.LastCommitSeq != 1 {
		t.Fatalf("recovery=%+v", res)
	}
	visible, _, err := store.Visible(contentFP)
	if err != nil {
		t.Fatalf("rolled-forward index not visible: %v", err)
	}
	if visible.Digest != idx.Digest {
		t.Fatal("digest mismatch after roll-forward")
	}
}
