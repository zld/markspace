package recovery_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/recovery"
)

func newSource(t *testing.T, name string, data []byte) *asset.Source {
	t.Helper()
	return asset.FromBytes(name, data)
}

func TestSaveLoadRoundTripIsStable(t *testing.T) {
	src := newSource(t, "a.bin", []byte(strings.Repeat("z", 10000)))
	cfg := pipeline.Config{ChunkSize: 4096, QueueSize: 2}
	cp := recovery.New(src, cfg)
	sess, err := recovery.Open(src, cp, cfg, "scan", "emit")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scan", "emit"} {
		sess.MarkRunning(name)
		for off := int64(4096); off <= 10000; off += 4096 {
			if off > 10000 {
				off = 10000
			}
			if err := sess.Commit(name, off); err != nil {
				t.Fatal(err)
			}
		}
		sess.Finish(name, "ok")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	sum1, err := cp.Save(path)
	if err != nil {
		t.Fatal(err)
	}
	sum2, err := cp.Save(path)
	if err != nil || sum1 != sum2 {
		t.Fatalf("digest not stable: %q %q %v", sum1, sum2, err)
	}
	loaded, ok, err := recovery.Load(path)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if loaded.SourceDigest != src.Digest || !loaded.Complete("scan", "emit") {
		t.Errorf("loaded checkpoint lost state: %+v", loaded)
	}
	again, err := loaded.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := cp.Marshal()
	if string(again) != string(first) {
		t.Error("checkpoint marshalling is not stable across a load/save cycle")
	}
}

func TestResumeFromVerifiedBoundary(t *testing.T) {
	data := []byte(strings.Repeat("abcdefghij", 1000)) // 10 000 bytes
	src := newSource(t, "resume.bin", data)
	cfg := pipeline.Config{ChunkSize: 1024, QueueSize: 2}
	cp := recovery.New(src, cfg)
	sess, err := recovery.Open(src, cp, cfg, "scan")
	if err != nil {
		t.Fatal(err)
	}
	for off := int64(2048); off <= 6144; off += 2048 {
		if err := sess.Commit("scan", off); err != nil {
			t.Fatal(err)
		}
	}
	sess.MarkRunning("scan")
	path := filepath.Join(t.TempDir(), "cp.json")
	if _, err := cp.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := recovery.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := recovery.Open(src, loaded, cfg, "scan")
	if err != nil {
		t.Fatalf("resume rejected: %v", err)
	}
	if got := restarted.Boundary("scan"); got != 6144 {
		t.Errorf("resume boundary = %d, want 6144", got)
	}
	lowest, per := restarted.ResumePlan("scan", "emit")
	if lowest != 0 || per["emit"] != 0 || per["scan"] != 6144 {
		t.Errorf("resume plan = %d %+v", lowest, per)
	}
	// Resuming and committing again must work from the stored boundary.
	if err := restarted.Commit("scan", 10000); err != nil {
		t.Fatal(err)
	}
	if restarted.Boundary("scan") != 10000 {
		t.Error("commit after resume did not advance")
	}
}

func TestSourceDigestChangeRejectsCheckpoint(t *testing.T) {
	cfg := pipeline.Config{ChunkSize: 1024, QueueSize: 1}
	original := newSource(t, "a.bin", []byte("original content"))
	cp := recovery.New(original, cfg)
	sess, err := recovery.Open(original, cp, cfg, "scan")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit("scan", 8); err != nil {
		t.Fatal(err)
	}
	mutated := newSource(t, "a.bin", []byte("mutated content"))
	_, err = recovery.Open(mutated, cp, cfg, "scan")
	if !fault.HasKind(err, fault.KindConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "digest changed") {
		t.Errorf("error should name the digest change: %v", err)
	}
	// A different pipeline shape is also rejected.
	wider := pipeline.Config{ChunkSize: 2048, QueueSize: 1}
	if _, err := recovery.Open(original, cp, wider, "scan"); !fault.HasKind(err, fault.KindConflict) {
		t.Errorf("config change should conflict: %v", err)
	}
	// A tampered prefix digest is rejected too.
	tampered := *cp
	st := *cp.Stage("scan")
	st.Digest = strings.Repeat("0", 64)
	tampered.Stages = map[string]*recovery.StageState{"scan": &st}
	if _, err := recovery.Open(original, &tampered, cfg, "scan"); !fault.HasKind(err, fault.KindConflict) {
		t.Errorf("tampered prefix should conflict: %v", err)
	}
}

func TestCommitMonotonic(t *testing.T) {
	src := newSource(t, "m.bin", make([]byte, 4096))
	cfg := pipeline.DefaultConfig()
	cp := recovery.New(src, cfg)
	sess, err := recovery.Open(src, cp, cfg, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit("s", 2048); err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit("s", 1024); !fault.HasKind(err, fault.KindInvalid) {
		t.Errorf("regressing commit should fail: %v", err)
	}
	if err := sess.Commit("s", 99999); !fault.HasKind(err, fault.KindBoundary) {
		t.Errorf("commit past end should fail: %v", err)
	}
	if err := sess.CommitChunk("s", pipeline.Chunk{Offset: 2048, Data: make([]byte, 100)}); err != nil {
		t.Fatal(err)
	}
	if sess.Boundary("s") != 2148 {
		t.Errorf("boundary = %d", sess.Boundary("s"))
	}
	sess.Fail("s", fault.New(fault.StagePipeline, fault.KindCancelled, "m.bin", 50, "cancelled"))
	snap := sess.Snapshot()
	if len(snap) != 1 || snap[0].Status != recovery.StatusFailed || snap[0].Offset != 50 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestLoadMissingAndCorrupt(t *testing.T) {
	if _, ok, err := recovery.Load(filepath.Join(t.TempDir(), "nope.json")); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v", ok, err)
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recovery.Load(path); !fault.HasKind(err, fault.KindCorrupt) {
		t.Errorf("corrupt checkpoint: %v", err)
	}
}

func TestCheckpointJSONHasNoMapOrdering(t *testing.T) {
	src := newSource(t, "o.bin", []byte("abc"))
	cfg := pipeline.DefaultConfig()
	cp := recovery.New(src, cfg)
	cp.Stage("zeta")
	cp.Stage("alpha")
	cp.Stage("mike")
	data, err := cp.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Stages []struct {
			Name string `json:"name"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "mike", "zeta"}
	for i, s := range probe.Stages {
		if s.Name != want[i] {
			t.Errorf("stage %d = %q, want %q", i, s.Name, want[i])
		}
	}
}
