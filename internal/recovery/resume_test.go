package recovery

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"encoding/base64"
)

func begin(t *testing.T, s *Store, input, params string) bool {
	t.Helper()
	resumed, err := s.Begin(input, params)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return resumed
}

func TestCommitAndResumeFromFirstUnfinishedStage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	s1, err := NewStore(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if begin(t, s1, "input-a", "params-a") {
		t.Fatal("fresh checkpoint reported a resume")
	}
	if err := s1.Commit("ingest", []byte(`{"cues":20}`), 20); err != nil {
		t.Fatal(err)
	}
	if err := s1.Commit("normalize", []byte(`{"points":40}`), 40); err != nil {
		t.Fatal(err)
	}

	s2, err := NewStore(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if !begin(t, s2, "input-a", "params-a") {
		t.Fatal("expected resume")
	}
	if got := s2.CompletedStages(); len(got) != 2 || got[0] != "ingest" || got[1] != "normalize" {
		t.Fatalf("completed=%v", got)
	}
	if payload, ok, err := s2.Lookup("normalize"); err != nil || !ok || string(payload) != `{"points":40}` {
		t.Fatalf("lookup payload=%s ok=%v err=%v", payload, ok, err)
	}
	if _, ok, _ := s2.Lookup("index"); ok {
		t.Fatal("index stage must not be committed yet")
	}
	if err := s2.Commit("index", []byte(`{"gaps":4}`), 44); err != nil {
		t.Fatal(err)
	}
	cp, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastComplete != "index" || cp.LastRecordID != 44 {
		t.Fatalf("cp=%+v", cp)
	}
}

func TestResumeRejectsDifferentInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	s, _ := NewStore(dir, path)
	begin(t, s, "input-a", "params-a")
	if err := s.Commit("ingest", []byte(`{}`), 0); err != nil {
		t.Fatal(err)
	}
	s2, _ := NewStore(dir, path)
	if _, err := s2.Begin("input-b", "params-a"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("want ErrDigestMismatch, got %v", err)
	}
}

func TestResumeRejectsDifferentParams(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	s, _ := NewStore(dir, path)
	begin(t, s, "input-a", "params-a")
	if err := s.Commit("ingest", []byte(`{}`), 0); err != nil {
		t.Fatal(err)
	}
	s2, _ := NewStore(dir, path)
	if _, err := s2.Begin("input-a", "params-b"); err == nil {
		t.Fatal("want params mismatch")
	}
}

func TestCorruptPayloadIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	s, _ := NewStore(dir, path)
	begin(t, s, "input-a", "params-a")
	if err := s.Commit("ingest", []byte(`{"a":1}`), 1); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Payloads are stored base64-encoded; swap in a different payload while
	// leaving the recorded digest untouched.
	orig := base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))
	forged := base64.StdEncoding.EncodeToString([]byte(`{"a":2}`))
	tampered := bytes.Replace(raw, []byte(orig), []byte(forged), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatalf("payload not found in checkpoint %s", raw)
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("want ErrCorruptCheckpoint, got %v", err)
	}
}

func TestCheckpointPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewStore(dir, filepath.Join(dir, "..", "escape.json")); err == nil {
		t.Fatal("want traversal rejection")
	}
	if _, err := NewStore(dir, filepath.Join(dir, "sub", "ok.json")); err != nil {
		t.Fatalf("nested path should be allowed: %v", err)
	}
}

func TestInMemoryStore(t *testing.T) {
	s, err := NewStore("", "")
	if err != nil {
		t.Fatal(err)
	}
	if begin(t, s, "in", "p") {
		t.Fatal("in-memory store cannot resume across processes")
	}
	if err := s.Commit("ingest", []byte(`x`), 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lookup("ingest"); err != nil || !ok {
		t.Fatalf("lookup ok=%v err=%v", ok, err)
	}
	if s.Commits() != 1 {
		t.Fatalf("commits=%d", s.Commits())
	}
}

func TestVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"input_digest":"a","params_digest":"b","stages":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("want version error")
	}
}
