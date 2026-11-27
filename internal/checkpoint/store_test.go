package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"example.com/continuity/internal/fault"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if s.Exists() {
		t.Fatal("new store must be empty")
	}
	state := &State{
		Version: 1, InputDigest: "in1", ConfigDigest: "cfg1",
		InputOffset: 512, FrameCount: 8,
		Stages:  map[string]int64{fault.StageSegment: 512, fault.StageSilence: 256},
		Outputs: []string{"manifest.json"}, OutputDigest: "abc",
	}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	if !s.Exists() {
		t.Fatal("checkpoint not committed")
	}
	got, err := s.Load("in1", "cfg1")
	if err != nil {
		t.Fatal(err)
	}
	if got.InputOffset != 512 || got.FrameCount != 8 || got.Stages[fault.StageSilence] != 256 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// No temp file left after a clean commit.
	if _, err := readFileInfo(s.tmpPath()); !errors.Is(err, errNotExist) {
		t.Fatalf("temp file leaked: %v", err)
	}
}

func TestStaleCheckpointRejected(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(&State{Version: 1, InputDigest: "in1", ConfigDigest: "cfg1", InputOffset: 10}); err != nil {
		t.Fatal(err)
	}
	// Same input, changed config must be rejected.
	if _, err := s.Load("in1", "cfg2"); !errors.Is(err, fault.ErrStaleCheckpoint) {
		t.Fatalf("config change: %v", err)
	}
	// Changed input must be rejected too.
	if _, err := s.Load("in2", "cfg1"); !errors.Is(err, fault.ErrStaleCheckpoint) {
		t.Fatalf("input change: %v", err)
	}
}

func TestCleanTempAndReset(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(&State{Version: 1, InputDigest: "i", ConfigDigest: "c"}); err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted write leaving a temp file behind.
	if err := writeTemp(s.tmpPath()); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanTemp(); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileInfo(s.tmpPath()); !errors.Is(err, errNotExist) {
		t.Fatal("temp not cleaned")
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if s.Exists() {
		t.Fatal("reset did not remove checkpoint")
	}
	// CleanTemp on a nonexistent temp must be a no-op.
	if err := s.CleanTemp(); err != nil {
		t.Fatal(err)
	}
}

func TestDeterministicStateBytes(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	st := func(off int64) *State {
		return &State{Version: 1, InputDigest: "i", ConfigDigest: "c", InputOffset: off,
			Stages: map[string]int64{}, Outputs: []string{}}
	}
	if err := s.Save(st(1)); err != nil {
		t.Fatal(err)
	}
	first, err := readFileBytes(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(st(1)); err != nil {
		t.Fatal(err)
	}
	second, err := readFileBytes(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("identical state produced different bytes")
	}
}

func TestDigestOf(t *testing.T) {
	if DigestOf(nil) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("empty digest wrong")
	}
}

// small filesystem helpers isolated from production code.
var errNotExist = os.ErrNotExist

func readFileInfo(p string) (interface{}, error) {
	if _, err := os.Stat(p); err != nil {
		return nil, errNotExist
	}
	return struct{}{}, nil
}

func readFileBytes(p string) ([]byte, error) {
	return os.ReadFile(p)
}

func writeTemp(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte("partial"), 0o644)
}
