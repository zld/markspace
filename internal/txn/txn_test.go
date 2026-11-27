package txn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALDoneAndReset(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Done(StageParse) {
		t.Fatal("empty WAL reports done")
	}
	if err := w.Log(StageParse, "begin", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.Log(StageParse, "done", "events=6"); err != nil {
		t.Fatal(err)
	}
	// reload from disk
	w2, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !w2.Done(StageParse) || w2.Done(StageBuild) {
		t.Fatalf("reloaded WAL records = %+v", w2.Records)
	}
	if err := w2.Reset(); err != nil {
		t.Fatal(err)
	}
	if w2.Done(StageParse) {
		t.Fatal("reset WAL still reports done")
	}
}

func TestRecoverRollsBackIncompleteStages(t *testing.T) {
	dir := t.TempDir()
	w, _ := OpenWAL(dir)
	tx := &Txn{Dir: dir, WAL: w}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.Log(StageParse, "begin", ""))
	must(tx.WriteStage(StageParse, []byte(`[]`)))
	must(w.Log(StageParse, "done", ""))
	must(w.Log(StageBuild, "begin", ""))
	must(tx.WriteStage(StageBuild, []byte(`{"partial":`)))

	rec, err := tx.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Interrupted || rec.ResumeStage != StageMap {
		t.Fatalf("recovery = %+v, want interrupted resume at map", rec)
	}
	if rec.RolledBack != 1 {
		t.Fatalf("rolled back %d temp items, want 1 (build.json)", rec.RolledBack)
	}
	if _, err := os.Stat(filepath.Join(dir, "work", "tmp", "parse.json")); err != nil {
		t.Fatal("completed stage output was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "work", "tmp", "build.json")); !os.IsNotExist(err) {
		t.Fatal("incomplete stage temp file survived recovery")
	}
}

func TestRecoverCleanCommit(t *testing.T) {
	dir := t.TempDir()
	w, _ := OpenWAL(dir)
	for _, s := range Stages {
		w.Log(s, "done", "")
	}
	tx := &Txn{Dir: dir, WAL: w}
	rec, err := tx.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Interrupted || rec.ResumeStage != "none" || rec.RolledBack != 0 {
		t.Fatalf("recovery = %+v, want clean", rec)
	}
}

func TestCommitAtomic(t *testing.T) {
	dir := t.TempDir()
	w, _ := OpenWAL(dir)
	tx := &Txn{Dir: dir, WAL: w}
	if err := tx.CommitAtomic("result.json", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil || string(data) != `{"v":1}` {
		t.Fatalf("committed data = %q, %v", data, err)
	}
	// recommit replaces atomically; no duplicate or temp leftovers
	if err := tx.CommitAtomic("result.json", []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "result.json"))
	if string(data) != `{"v":2}` {
		t.Fatalf("recommitted data = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "work", "tmp", "commit.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp commit file left behind")
	}
}
