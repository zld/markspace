package task

import (
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/model"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	payload := []byte("stage-output")
	if err := s.Save("task1", model.StageDemux, payload); err != nil {
		t.Fatal(err)
	}
	got, cp, err := s.Load("task1", model.StageDemux)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) || !cp.Done || cp.Stage != model.StageDemux {
		t.Fatalf("bad load: %q %+v", got, cp)
	}
	if st, ok := s.LastValid("task1"); !ok || st != model.StageDemux {
		t.Fatalf("LastValid = %v %v", st, ok)
	}
}

func TestCorruptCheckpointRollsBack(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	for _, st := range StageOrder[:2] {
		if err := s.Save("t", st, []byte("v1-"+string(st))); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt the demux payload without touching its digest.
	bin := filepath.Join(dir, "t", "demux.bin")
	if err := os.WriteFile(bin, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	res, err := s.RunStage("t", model.StageDemux, func() ([]byte, error) {
		calls++
		return []byte("v2"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !res.Redone || res.Resumed || string(res.Payload) != "v2" {
		t.Fatalf("unexpected result: %+v calls=%d", res, calls)
	}
	// Downstream stage must have been invalidated.
	if s.HasCheckpoint("t", model.StageMap) {
		t.Fatal("map checkpoint should be invalidated")
	}
}

func TestResumeUsesCheckpoint(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Save("t", model.StageRemux, []byte("done"))
	res, err := s.RunStage("t", model.StageRemux, func() ([]byte, error) {
		t.Fatal("fn must not run when checkpoint is valid")
		return nil, nil
	})
	if err != nil || !res.Resumed || string(res.Payload) != "done" {
		t.Fatalf("unexpected: %+v err=%v", res, err)
	}
}

func TestUnsafeTaskKeyRejected(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, key := range []string{"../etc", "a/b", "", "a b"} {
		if err := s.Save(key, model.StageDemux, []byte("x")); err == nil {
			t.Fatalf("key %q should be rejected", key)
		}
	}
}
