package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediapipeline/internal/evidence"
)

func TestJournalRecoverPublishedWithoutRepublish(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("published artifact bytes")
	sum := evidence.SHA256Bytes(content)
	target := filepath.Join(dir, "products", "a.mp4")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Prepare(Intent{Key: "key1", TargetPath: target, SHA256: sum, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	j2, err := Open(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := j2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Recovered != 1 || stats.Missing != 0 || stats.Corrupt != 0 || stats.Duplicates != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	stats2, err := j2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Recovered != 0 {
		t.Fatalf("committed intent recovered again: %+v", stats2)
	}
}

func TestJournalDetectsMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(filepath.Join(dir, "j.jsonl"))
	missing := filepath.Join(dir, "missing.mp4")
	if _, err := j.Prepare(Intent{Key: "missing", TargetPath: missing, SHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "corrupt.mp4")
	if err := os.WriteFile(corrupt, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Prepare(Intent{Key: "corrupt", TargetPath: corrupt, SHA256: strings.Repeat("b", 64), Size: 99}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j2, _ := Open(filepath.Join(dir, "j.jsonl"))
	stats, err := j2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Missing != 1 || stats.Corrupt != 1 || len(stats.NeedsWork) != 2 {
		t.Fatalf("stats=%+v", stats)
	}
	q, err := Quarantine(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Fatalf("corrupt target still present: %v", err)
	}
	if _, err := os.Stat(q); err != nil {
		t.Fatalf("quarantine missing: %v", err)
	}
}

func TestAtomicWriteAndPublish(t *testing.T) {
	dir := t.TempDir()
	tmp, sum, n, err := WriteTempSynced(dir, "*.tmp", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || sum != evidence.SHA256Bytes([]byte("hello")) {
		t.Fatalf("n=%d sum=%s", n, sum)
	}
	target := filepath.Join(dir, "out.mp4")
	if err := Publish(tmp, target); err != nil {
		t.Fatal(err)
	}
	got, _, err := evidence.SHA256File(target)
	if err != nil || got != sum {
		t.Fatalf("published hash=%s err=%v", got, err)
	}
}
