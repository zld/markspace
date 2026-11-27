package journal

import (
	"testing"

	"continuity/internal/model"
)

func TestLifecycleTransitions(t *testing.T) {
	j := openTestJournal(t)
	order := []State{StatePlanned, StateProcessing, StateProcessed, StateValidated}
	for _, s := range order {
		if err := j.Transition(1, 1, s, ""); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
		if j.StateOf(1) != s {
			t.Fatalf("state = %s, want %s", j.StateOf(1), s)
		}
	}
}

func TestBackwardTransitionRejected(t *testing.T) {
	j := openTestJournal(t)
	if err := j.Transition(1, 1, StateProcessed, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Transition(1, 1, StatePlanned, ""); err == nil {
		t.Fatal("backward transition processed -> planned allowed")
	}
}

func TestFailedIsTerminal(t *testing.T) {
	j := openTestJournal(t)
	if err := j.Transition(1, 1, StateProcessing, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Transition(1, 1, StateFailed, "x"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []State{StateProcessing, StateProcessed, StateValidated} {
		if err := j.Transition(1, 1, s, ""); err == nil {
			t.Fatalf("failed -> %s allowed", s)
		}
	}
	// Re-recording the failed reason is permitted.
	if err := j.Transition(1, 1, StateFailed, "again"); err != nil {
		t.Fatalf("failed -> failed should be allowed: %v", err)
	}
}

func TestResumePointAndReset(t *testing.T) {
	j := openTestJournal(t)
	for n := 1; n <= 5; n++ {
		num := model.FrameNumber(n)
		_ = j.Transition(num, 1, StatePlanned, "")
		_ = j.Transition(num, 1, StateProcessing, "")
		_ = j.Transition(num, 1, StateProcessed, "")
		_ = j.Transition(num, 1, StateValidated, "")
	}
	// Frames 6 and 7 are in flight but never validated.
	for n := 6; n <= 7; n++ {
		num := model.FrameNumber(n)
		_ = j.Transition(num, 1, StatePlanned, "")
		_ = j.Transition(num, 1, StateProcessing, "")
	}
	if rp := j.ResumePoint(7); rp != 6 {
		t.Fatalf("resume point = %d, want 6", rp)
	}
	if err := j.ResetInProgress(); err != nil {
		t.Fatalf("ResetInProgress: %v", err)
	}
	if j.StateOf(6) != StatePlanned || j.StateOf(7) != StatePlanned {
		t.Fatal("in-flight frames not rolled back to planned")
	}
	if j.StateOf(5) != StateValidated {
		t.Fatal("validated prefix must survive reset")
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	root := t.TempDir()
	j, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Transition(2, 1, StateProcessed, ""); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.StateOf(2) != StateProcessed {
		t.Fatal("checkpoint did not persist state")
	}
	counts := reloaded.CumulativeCounts()
	if counts[StateDiscovered] != 1 || counts[StateProcessed] != 1 || counts[StateValidated] != 0 {
		t.Fatalf("cumulative counts wrong: %+v", counts)
	}
}

func openTestJournal(t *testing.T) *Journal {
	t.Helper()
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return j
}
