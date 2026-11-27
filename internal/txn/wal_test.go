package txn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendCommitAndReplay(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"units":4}`)
	e, err := m.WAL().Append(Entry{
		Stage: StageScan, Track: "v", InputSHA256: "abc",
		Cursor: 128, Summary: []byte(`{"k":1}`),
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	// Before commit the artifact must only exist as a temp file.
	if _, err := os.Stat(filepath.Join(dir, e.OutputFile)); !os.IsNotExist(err) {
		t.Fatal("final artifact must not exist before commit")
	}
	if _, err := os.Stat(filepath.Join(dir, e.OutputFile+".tmp")); err != nil {
		t.Fatalf("staged temp missing: %v", err)
	}
	if e.OutputSHA != HashBytes(payload) {
		t.Fatal("output hash not recorded correctly")
	}
	if err := m.WAL().Commit(StageScan, "v"); err != nil {
		t.Fatal(err)
	}
	if !m.WAL().IsCommitted(StageScan, "v") {
		t.Fatal("commit not visible")
	}
	// Idempotent: a second commit must not re-do work or error.
	if err := m.WAL().Commit(StageScan, "v"); err != nil {
		t.Fatalf("recommit must be idempotent: %v", err)
	}

	// Reopen: committed entry replays, checkpoint cross-checks succeed.
	m2, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.WAL().IsCommitted(StageScan, "v") {
		t.Fatal("commit did not survive reopen")
	}
	got := m2.WAL().CommittedEntries()
	if len(got) != 1 || got[0].OutputSHA != e.OutputSHA {
		t.Fatalf("replayed entries wrong: %+v", got)
	}
}

func TestUncommittedArtifactsDiscarded(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	// One committed stage.
	e1, err := m.WAL().Append(Entry{Stage: StageParse, Track: "v"}, []byte("good"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.WAL().Commit(StageParse, "v"); err != nil {
		t.Fatal(err)
	}
	// One stage appended but never committed (simulated crash).
	e2, err := m.WAL().Append(Entry{Stage: StageReport, Track: "v"}, []byte("half"))
	if err != nil {
		t.Fatal(err)
	}
	_ = e2
	// Reopen to replay the WAL and drop the uncommitted record.
	m2, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash that left a stray temp with no WAL record at all.
	stray := filepath.Join(dir, "report-9.out.tmp")
	if err := os.WriteFile(stray, []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := m2.DiscardUncommitted()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("sweep removed %d files, want 1", len(removed))
	}
	if _, err := os.Stat(filepath.Join(dir, e1.OutputFile)); err != nil {
		t.Fatalf("committed artifact must survive: %v", err)
	}
	if m2.WAL().IsCommitted(StageReport, "v") {
		t.Fatal("uncommitted stage must not replay as committed")
	}
}

func TestCheckpointFinalizeAndRecover(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.WAL().Append(Entry{Stage: StageScan, Track: "v",
		InputSHA256: "in", Cursor: 10}, []byte("scan-out")); err != nil {
		t.Fatal(err)
	}
	if err := m.WAL().Commit(StageScan, "v"); err != nil {
		t.Fatal(err)
	}
	segs := map[string]string{"seg-000": HashBytes([]byte("seg0"))}
	cp, err := m.Finalize("run-1", "in", segs, "2026-11-28T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Stages) != 1 || cp.SegmentHashes["seg-000"] == "" {
		t.Fatalf("checkpoint content wrong: %+v", cp)
	}
	// Reload and validate cross-referenced recovery.
	m2, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := m2.LoadCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.RunID != "run-1" || len(loaded.Stages) != 1 {
		t.Fatalf("recovered checkpoint wrong: %+v", loaded)
	}
}

func TestRecoverDetectsTamperedStage(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := m.WAL().Append(Entry{Stage: StageScan, Track: "v"}, []byte("orig"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.WAL().Commit(StageScan, "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Finalize("r", "in", nil, "now"); err != nil {
		t.Fatal(err)
	}
	// Tamper with the committed artifact after the checkpoint was written.
	if err := os.WriteFile(filepath.Join(dir, e.OutputFile), []byte("TAMPERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Finalize must now fail verification rather than commit silently.
	if _, err := m.Finalize("r2", "in", nil, "now2"); err == nil {
		t.Fatal("tampered artifact must fail pre-commit verification")
	}
}

func TestCorruptWALRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wal.jsonl"),
		[]byte("{not valid json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWAL(dir); err == nil {
		t.Fatal("corrupt WAL must be a hard recovery error")
	}
}

func TestWALSizeCap(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, MaxWALBytes+1)
	if _, err := w.Append(Entry{Stage: StageScan}, big); err == nil {
		t.Fatal("oversized artifact must be rejected")
	}
}
