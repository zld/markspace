package recover

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
)

func seg(layer string, seq int64, digest string) SegmentState {
	return SegmentState{Layer: layer, Sequence: seq, Path: layer + "/seg.m4s",
		InputDigest: digest, OutputDigest: "out", Bytes: 100, Samples: 10, Complete: true}
}

func TestCommitAndReuseAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	cp := New("run1", "/tmp/root", "clean")
	if err := cp.Commit(seg("a1", 1, "hash-a")); err != nil {
		t.Fatal(err)
	}
	if err := cp.Commit(seg("a1", 2, "hash-b")); err != nil {
		t.Fatal(err)
	}
	if err := cp.Save(dir); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: load and reuse unchanged, rescan the changed one.
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Reuse("a1", 1, "hash-a"); !ok {
		t.Fatal("unchanged segment should be reusable")
	}
	if _, ok := loaded.Reuse("a1", 2, "hash-CHANGED"); ok {
		t.Fatal("changed digest must force a rescan")
	}
	if _, ok := loaded.Reuse("a1", 3, "hash-a"); ok {
		t.Fatal("uncommitted sequence must not be reused")
	}
}

func TestIncompleteNeverCommitted(t *testing.T) {
	cp := New("r", "x", "c")
	s := seg("a1", 1, "h")
	s.Complete = false
	if err := cp.Commit(s); !errors.Is(err, fault.ErrFormat) {
		t.Fatalf("want refusal, got %v", err)
	}
	if len(cp.Done) != 0 {
		t.Fatal("incomplete segment was committed")
	}
}

func TestIsolateLayer(t *testing.T) {
	cp := New("r", "x", "corrupt")
	cp.Commit(seg("a1", 1, "h1"))
	cp.Commit(seg("a2", 1, "h2"))
	cp.Isolate(LayerState{Layer: "a1", LastSeq: 1, Isolated: true, Reason: "corrupt", Offset: 4096})
	if !cp.Layers["a1"].Isolated || cp.Layers["a1"].Offset != 4096 {
		t.Fatalf("isolation not recorded: %+v", cp.Layers["a1"])
	}
	if cp.Layers["a2"].Isolated {
		t.Fatal("a2 should keep running")
	}
}

func TestSaveIsDeterministic(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	build := func(dir string) []byte {
		cp := New("r", "x", "c")
		cp.Commit(seg("a2", 1, "2"))
		cp.Commit(seg("a1", 2, "1b"))
		cp.Commit(seg("a1", 1, "1a"))
		if err := cp.Save(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, FileName))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if string(build(dirA)) != string(build(dirB)) {
		t.Fatal("checkpoint bytes depend on commit order")
	}
}

func TestLoadBadJSONIsRecoverable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o644)
	_, err := Load(dir)
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("want corrupt, got %v", err)
	}
	if fe, _ := fault.As(err); !fe.Recoverable {
		t.Fatal("a bad checkpoint should be recoverable by rescanning")
	}
}

func TestResetLayer(t *testing.T) {
	cp := New("r", "x", "c")
	cp.Commit(seg("a1", 1, "1"))
	cp.Commit(seg("a1", 2, "2"))
	cp.Commit(seg("a2", 1, "3"))
	cp.Reset("a1")
	if _, ok := cp.Done0("a1", 1); ok {
		t.Fatal("a1 not reset")
	}
	if _, ok := cp.Done0("a2", 1); !ok {
		t.Fatal("a2 should be untouched")
	}
}
