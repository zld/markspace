package task

import (
	"strings"
	"testing"

	"bitrack/internal/model"
)

func TestLegalTransitions(t *testing.T) {
	cases := []struct {
		from, to model.SegmentState
	}{
		{model.StateQueued, model.StateRunning},
		{model.StateQueued, model.StatePaused},
		{model.StateRunning, model.StateValidating},
		{model.StateRunning, model.StateRetryable},
		{model.StateValidating, model.StateCommitted},
		{model.StateValidating, model.StateRetryable},
		{model.StateRetryable, model.StateQueued},
		{model.StatePaused, model.StateQueued},
		{model.StateFailed, model.StateQueued},
	}
	for _, c := range cases {
		if !CanTransition(c.from, c.to) {
			t.Errorf("expected legal transition %s -> %s", c.from, c.to)
		}
	}
}

func TestIllegalTransitions(t *testing.T) {
	cases := []struct {
		from, to model.SegmentState
	}{
		{model.StateQueued, model.StateCommitted},
		{model.StateCommitted, model.StateRunning},
		{model.StateFailed, model.StateCommitted},
		{model.StatePaused, model.StateValidating},
		{model.StateValidating, model.StateQueued},
	}
	for _, c := range cases {
		if CanTransition(c.from, c.to) {
			t.Errorf("expected illegal transition %s -> %s", c.from, c.to)
		}
		seg := &model.Segment{Index: 2, State: c.from}
		err := Move(seg, c.to)
		if err == nil {
			t.Fatalf("Move(%s->%s) should fail", c.from, c.to)
		}
		if !strings.Contains(err.Error(), "segment 2") {
			t.Errorf("error should name segment index: %v", err)
		}
		if seg.State != c.from {
			t.Errorf("failed move must not change state, got %s", seg.State)
		}
	}
}

func TestMoveAndReopen(t *testing.T) {
	seg := &model.Segment{Index: 0, State: model.StateQueued}
	if err := Move(seg, model.StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := Move(seg, model.StateValidating); err != nil {
		t.Fatal(err)
	}
	if err := Move(seg, model.StateCommitted); err != nil {
		t.Fatal(err)
	}
	if !IsTerminal(seg.State) {
		t.Errorf("committed should be terminal")
	}
	failed := &model.Segment{Index: 1, State: model.StateFailed, LastError: "boom"}
	if err := Reopen(failed); err != nil {
		t.Fatal(err)
	}
	if failed.State != model.StateQueued || failed.LastError != "" {
		t.Errorf("reopen failed: state=%s err=%q", failed.State, failed.LastError)
	}
}
