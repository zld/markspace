package state

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.state")
	want := Checkpoint{
		Phase:           "commit",
		CommittedEvents: 5,
		LastTimestampNs: 45_000_000,
		InputOffset:     5,
		Digest:          0xDEADBEEF,
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := Load(path)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadMissing(t *testing.T) {
	_, ok, err := Load(filepath.Join(t.TempDir(), "nope.state"))
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestSaveOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.state")
	if err := Save(path, Checkpoint{Phase: "commit", CommittedEvents: 5}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Checkpoint{Phase: "done", CommittedEvents: 142}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(path)
	if err != nil || !ok || got.Phase != "done" || got.CommittedEvents != 142 {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
}
