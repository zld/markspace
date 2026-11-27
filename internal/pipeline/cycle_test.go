package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"medtrace/internal/fixture"
	"medtrace/internal/mederr"
)

func TestVerifyCycleDescriptor(t *testing.T) {
	spec := &CycleSpec{Tracks: fixture.CycleTracks()}
	_, err := VerifyCycle(spec)
	se := mederr.AsError(err)
	if se == nil || se.Category != mederr.CatCycle {
		t.Fatalf("want dependency_cycle, got %v", err)
	}
}

func TestLoadCycleSpecMissingIsNotError(t *testing.T) {
	spec, err := LoadCycleSpec(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || spec != nil {
		t.Fatalf("missing spec must be (nil,nil), got %+v err=%v", spec, err)
	}
}

func TestLoadCycleSpecRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cycle.json")
	spec := CycleSpec{Tracks: fixture.CycleTracks()}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCycleSpec(p)
	if err != nil || got == nil || len(got.Tracks) != 4 {
		t.Fatalf("round trip failed: %+v err=%v", got, err)
	}
}
