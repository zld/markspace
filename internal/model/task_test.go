package model

import (
	"errors"
	"testing"
)

func TestStatusTransitions(t *testing.T) {
	cases := []struct {
		name string
		from Status
		to   Status
		ok   bool
	}{
		{"queued->running", StatusQueued, StatusRunning, true},
		{"running->succeeded", StatusRunning, StatusSucceeded, true},
		{"running->failed", StatusRunning, StatusFailed, true},
		{"running->interrupted", StatusRunning, StatusInterrupted, true},
		{"queued->interrupted", StatusQueued, StatusInterrupted, true},
		{"interrupted->queued", StatusInterrupted, StatusQueued, true},
		{"failed->queued", StatusFailed, StatusQueued, true},
		{"succeeded->queued (invalidation)", StatusSucceeded, StatusQueued, true},
		{"queued->succeeded", StatusQueued, StatusSucceeded, false},
		{"succeeded->running", StatusSucceeded, StatusRunning, false},
		{"failed->running", StatusFailed, StatusRunning, false},
		{"interrupted->succeeded", StatusInterrupted, StatusSucceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.from
			err := s.Transition(tc.to)
			if tc.ok && err != nil {
				t.Fatalf("expected legal, got %v", err)
			}
			if !tc.ok {
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("expected ErrIllegalTransition, got %v", err)
				}
				if s != tc.from {
					t.Fatalf("state changed on rejected transition")
				}
			}
		})
	}
}

func TestResetToQueued(t *testing.T) {
	seg := &Segment{Status: StatusSucceeded}
	if err := seg.ResetToQueued(); err != nil || seg.Status != StatusQueued {
		t.Fatalf("succeeded reset failed: %v", err)
	}
	seg.Status = StatusRunning
	if err := seg.ResetToQueued(); err == nil {
		t.Fatal("running segment must not reset directly")
	}
}

func TestMarkInterruptedKeepsTerminal(t *testing.T) {
	seg := &Segment{Status: StatusSucceeded}
	seg.MarkInterrupted()
	if seg.Status != StatusSucceeded {
		t.Fatal("terminal state must be preserved")
	}
	seg.Status = StatusRunning
	seg.MarkInterrupted()
	if seg.Status != StatusInterrupted {
		t.Fatal("running segment must become interrupted")
	}
}
