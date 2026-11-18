package main

import (
	"errors"
	"testing"

	"relaylab/internal/signaling"
)

func TestClassifyError(t *testing.T) {
	signalingPhases := []string{"signaling", "offer", "answer", "offer-delivery", "answer-delivery"}
	for _, ph := range signalingPhases {
		err := &signaling.PhaseError{Phase: ph, Err: errors.New("boom")}
		if got := classifyError(err); got != exitArgOrSignaling {
			t.Errorf("phase %q -> exit %d, want %d", ph, got, exitArgOrSignaling)
		}
	}
	transportPhases := []string{"ice", "track", "transport", "candidates", "peer-a", "peer-b"}
	for _, ph := range transportPhases {
		err := &signaling.PhaseError{Phase: ph, Err: errors.New("boom")}
		if got := classifyError(err); got != exitTransport {
			t.Errorf("phase %q -> exit %d, want %d", ph, got, exitTransport)
		}
	}
	// Non-signaling, generic errors default to transport failure.
	if got := classifyError(errors.New("plain")); got != exitTransport {
		t.Errorf("plain error -> %d, want %d", got, exitTransport)
	}
}

func TestValidateFlags(t *testing.T) {
	if err := validateFlags(0, 10, 4, "drop", 0.1, 0.1); err == nil {
		t.Error("zero duration must be invalid")
	}
	if err := validateFlags(31*1e9, 10, 4, "drop", 0, 0); err == nil {
		t.Error("duration over 30s must be invalid")
	}
	if err := validateFlags(5*1e9, 0, 4, "drop", 0, 0); err == nil {
		t.Error("zero packets must be invalid")
	}
	if err := validateFlags(5*1e9, 10, 0, "drop", 0, 0); err == nil {
		t.Error("zero queue must be invalid")
	}
	if err := validateFlags(5*1e9, 10, 4, "evaporate", 0, 0); err == nil {
		t.Error("bad overflow mode must be invalid")
	}
	if err := validateFlags(5*1e9, 10, 4, "block", 1.5, 0); err == nil {
		t.Error("rate > 1 must be invalid")
	}
	if err := validateFlags(5*1e9, 10, 4, "drop", 0.2, 0.3); err != nil {
		t.Errorf("valid flags rejected: %v", err)
	}
}

func TestRunDispatch(t *testing.T) {
	if got := run(nil); got != exitArgOrSignaling {
		t.Errorf("no args -> %d, want %d", got, exitArgOrSignaling)
	}
	if got := run([]string{"bogus"}); got != exitArgOrSignaling {
		t.Errorf("unknown command -> %d, want %d", got, exitArgOrSignaling)
	}
	if got := run([]string{"help"}); got != exitOK {
		t.Errorf("help -> %d, want %d", got, exitOK)
	}
}
