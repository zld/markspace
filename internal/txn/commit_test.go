package txn

import (
	"errors"
	"testing"
)

func demoStages() []Stage {
	return []Stage{
		{Name: "stage-0", Run: func(map[string][]byte) ([]byte, error) {
			return []byte("output-0"), nil
		}},
		{Name: "stage-1", Run: func(map[string][]byte) ([]byte, error) {
			return []byte("output-1"), nil
		}},
		{Name: "stage-2", Run: func(map[string][]byte) ([]byte, error) {
			return []byte("output-2"), nil
		}},
	}
}

func TestCoordinatorFullRun(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	c := NewCoordinator(j, nil)
	state := map[string][]byte{}
	if err := c.Run(demoStages(), state); err != nil {
		t.Fatalf("run: %v", err)
	}
	if c.CommittedCount() != 3 {
		t.Fatalf("committed = %d, want 3", c.CommittedCount())
	}
	if string(state["stage-2"]) != "output-2" {
		t.Fatalf("state bag wrong: %q", state["stage-2"])
	}
}

func TestCoordinatorCrashAndResume(t *testing.T) {
	dir := t.TempDir()

	// First process: inject a crash in the pre-commit window of stage-1,
	// after its checksum is journaled but before the boundary is durable.
	j1, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(stage, step string) error {
		if stage == "stage-1" && step == "pre-commit" {
			return errors.New("injected power loss")
		}
		return nil
	}
	c1 := NewCoordinator(j1, hook)
	err = c1.Run(demoStages(), map[string][]byte{})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	if err := j1.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: stage-0 boundary must survive; stage-1 must not be committed.
	j2, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !j2.Committed("stage-0") {
		t.Fatal("stage-0 boundary lost across crash")
	}
	if j2.Committed("stage-1") {
		t.Fatal("stage-1 must have rolled back")
	}
	c2 := NewCoordinator(j2, nil)
	state := map[string][]byte{}
	if err := c2.Run(demoStages(), state); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if c2.CommittedCount() != 3 {
		t.Fatalf("after resume committed = %d, want 3", c2.CommittedCount())
	}
	if err := j2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorValidationFailureKeepsBoundary(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	stages := demoStages()
	stages[1].Validate = func([]byte) error {
		return errors.New("checksum assertion failed")
	}
	c := NewCoordinator(j, nil)
	err = c.Run(stages, map[string][]byte{})
	if err == nil {
		t.Fatal("validation failure must surface as an error")
	}
	if !j.Committed("stage-0") {
		t.Fatal("prior committed boundary must be preserved")
	}
	if j.Committed("stage-1") {
		t.Fatal("failed stage must not commit a fake success")
	}
}

func TestCoordinatorRepeatedRunSameOutputs(t *testing.T) {
	dir := t.TempDir()
	runOnce := func() map[string][]byte {
		j, err := OpenJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer j.Close()
		c := NewCoordinator(j, nil)
		state := map[string][]byte{}
		if err := c.Run(demoStages(), state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	first := runOnce()
	second := runOnce()
	for k, v := range first {
		if string(second[k]) != string(v) {
			t.Fatalf("state %q differs across runs: %q vs %q", k, v, second[k])
		}
	}
}
