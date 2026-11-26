package store

import (
	"os"
	"path/filepath"
	"testing"

	"replaypipe/internal/timebase"
)

func manifestWith(tag string) []byte {
	return []byte(`{"version":2,"tag":"` + tag + `"}`)
}

func TestCommitPublishesAtomically(t *testing.T) {
	root := t.TempDir()
	res, e := Commit(root, "manifest.json", manifestWith("a"), nil, CommitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if !res.Committed || res.ManifestSHA == "" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, JournalName)); !os.IsNotExist(err) {
		t.Fatal("journal was not cleaned up")
	}
}

func TestCommitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	data := manifestWith("a")
	if _, e := Commit(root, "manifest.json", data, nil, CommitOptions{}); e != nil {
		t.Fatal(e)
	}
	res, e := Commit(root, "manifest.json", data, nil, CommitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if !res.Idempotent {
		t.Fatalf("second commit not idempotent: %+v", res)
	}
	if res.PreviousSHA != res.ManifestSHA {
		t.Fatal("idempotent commit changed the hash")
	}
}

func TestCommitKeepsRollbackCopy(t *testing.T) {
	root := t.TempDir()
	first := manifestWith("a")
	if _, e := Commit(root, "manifest.json", first, nil, CommitOptions{}); e != nil {
		t.Fatal(e)
	}
	res, e := Commit(root, "manifest.json", manifestWith("b"), nil, CommitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	rollback, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(rollback) != string(first) {
		t.Fatal("rollback copy is not the previous manifest")
	}
}

func TestCommitRejectsDigestMismatch(t *testing.T) {
	root := t.TempDir()
	files := []File{{Name: "a.bin", Data: []byte("payload"), Sum: "deadbeef"}}
	_, e := Commit(root, "manifest.json", manifestWith("a"), files, CommitOptions{})
	if e == nil || e.Code != timebase.CodeDigestMismatch {
		t.Fatalf("want DIGEST_MISMATCH, got %v", e)
	}
	if _, err := os.Stat(filepath.Join(root, "a.bin")); !os.IsNotExist(err) {
		t.Fatal("asset was published despite digest mismatch")
	}
}

func TestCommitInterruptedThenRecover(t *testing.T) {
	root := t.TempDir()
	old := manifestWith("old")
	if _, e := Commit(root, "manifest.json", old, nil, CommitOptions{}); e != nil {
		t.Fatal(e)
	}
	_, e := Commit(root, "manifest.json", manifestWith("new"), nil, CommitOptions{FailBeforeSwitch: true})
	if e == nil || e.Code != timebase.CodeInterrupted {
		t.Fatalf("want INTERRUPTED, got %v", e)
	}
	cur, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != string(old) {
		t.Fatal("interrupted commit replaced the published manifest")
	}
	rollback, state := Recover(root, "manifest.json")
	if !rollback || state != "staged" {
		t.Fatalf("recover = %t %q", rollback, state)
	}
	res, e := Commit(root, "manifest.json", manifestWith("new"), nil, CommitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if !res.Committed {
		t.Fatalf("resume failed: %+v", res)
	}
}

func TestPublishedAssetsHashStable(t *testing.T) {
	root := t.TempDir()
	payload := []byte("clip-bytes")
	sum := sha256Hex(payload)
	files := []File{{Name: "clips/c1.mp4", Data: payload, Sum: sum}}
	if _, e := Commit(root, "manifest.json", manifestWith("a"), files, CommitOptions{}); e != nil {
		t.Fatal(e)
	}
	got, err := os.ReadFile(filepath.Join(root, "clips/c1.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(got) != sum {
		t.Fatal("published asset bytes changed")
	}
}
