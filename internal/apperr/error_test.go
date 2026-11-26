package apperr

import (
	"errors"
	"testing"
)

func TestStructuredErrorAndExitCodes(t *testing.T) {
	inner := New("E_SAMPLE_RANGE", "sample extends past mdat").
		WithStage("scan").
		WithTrack("cam1", "cam1.mp4").
		WithSample(7, 1234).
		WithRecoverable(false)

	if inner.Error() == "" {
		t.Fatal("error text is empty")
	}
	if inner.Sample != 7 || inner.ByteOffset != 1234 {
		t.Fatalf("sample location not retained: %#v", inner)
	}

	wrapped := errors.New("wrap: " + inner.Error()) //nolint:goerr113
	// A plain string wrap is not structured; use the native wrapping below.
	native := New("E_JOB", "runner failed")
	native.Message = inner.Error()
	_ = wrapped

	pe, ok := As(inner)
	if !ok || pe.Code != "E_SAMPLE_RANGE" {
		t.Fatalf("As failed: %#v %v", pe, ok)
	}
	if got := ExitCode(inner); got != ExitInvalid {
		t.Fatalf("sample range exit=%d want %d", got, ExitInvalid)
	}
	if got := ExitCode(New("E_TRACK_ISOLATED", "jump")); got != ExitIsolated {
		t.Fatalf("isolation exit=%d", got)
	}
	if got := ExitCode(New("E_FAULT_INJECTED", "fault")); got != ExitInterrupted {
		t.Fatalf("fault exit=%d", got)
	}
}
