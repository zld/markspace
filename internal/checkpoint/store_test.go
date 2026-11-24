package checkpoint

import (
	"errors"
	"path/filepath"
	"testing"

	"medcheck/internal/clock"
)

func TestAppendAndLoadLatestValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.jsonl")
	for i := 1; i <= 3; i++ {
		err := Append(path, State{Boundary: i, NextSequences: map[uint32]uint64{7: uint64(i * 10)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadLatestValid(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Boundary != 3 || got.State.NextSequences[7] != 30 {
		t.Fatalf("state=%+v", got.State)
	}
}

func TestCorruptCheckpointFallsBackToNearestBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.jsonl")
	for i := 1; i <= 3; i++ {
		if err := Append(path, State{Boundary: i * 5}); err != nil {
			t.Fatal(err)
		}
	}
	if err := CorruptLastRecord(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLatestValid(path)
	if err == nil {
		t.Fatal("expected explicit corruption error")
	}
	var me clock.MediaError
	if !errors.As(err, &me) || me.Code != clock.CodeCheckpointInvalid {
		t.Fatalf("err=%v", err)
	}
	if got.State.Boundary != 10 {
		t.Fatalf("nearest valid boundary=%d want 10", got.State.Boundary)
	}
	if len(got.Corrupt) != 1 {
		t.Fatalf("corrupt=%+v", got.Corrupt)
	}
}

func TestAllCheckpointsInvalidIsFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.jsonl")
	if err := Append(path, State{Boundary: 5}); err != nil {
		t.Fatal(err)
	}
	if err := CorruptLastRecord(path); err != nil {
		t.Fatal(err)
	}
	_, err := LoadLatestValid(path)
	var me clock.MediaError
	if !errors.As(err, &me) || me.Code != clock.CodeCheckpointNoValidBoundary {
		t.Fatalf("expected no-valid-boundary, got %v", err)
	}
}
