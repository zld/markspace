package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "ckpt.json"))
	st := &State{InputSHA256: "in", ParamsSHA256: "pa", Stage: StageAudio, Offset: 42}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.InputSHA256 != st.InputSHA256 || got.ParamsSHA256 != st.ParamsSHA256 ||
		got.Stage != st.Stage || got.Offset != st.Offset {
		t.Fatalf("got %+v want %+v", got, st)
	}
	// 临时文件不得残留
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "none.json"))
	st, err := s.Load()
	if err != nil || st != nil {
		t.Fatalf("missing checkpoint: %v %v", st, err)
	}
}

func TestVerifyMismatch(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "c.json"))
	st := &State{InputSHA256: "a", ParamsSHA256: "b"}
	if err := s.Verify(st, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(st, "x", "b"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("input mismatch: %v", err)
	}
	if err := s.Verify(st, "a", "y"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("params mismatch: %v", err)
	}
}

func TestCorruptCheckpoint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	s := NewStore(p)
	if err := s.Save(&State{Stage: StageRTP}); err != nil {
		t.Fatal(err)
	}
	// 截断检查点文件
	if err := os.Truncate(p, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("corrupt checkpoint must fail to load")
	}
}
