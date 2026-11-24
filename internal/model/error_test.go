package model

import (
	"errors"
	"testing"
)

func TestStreamErrorFormatAndContext(t *testing.T) {
	base := New(StageParse, SeverityFatal, "bad sync").At(0x0100, 376)
	got := base.Error()
	for _, want := range []string{"parse", "fatal", "0x0100", "376", "bad sync"} {
		if !contains(got, want) {
			t.Fatalf("error %q missing %q", got, want)
		}
	}
}

func TestStreamErrorWrapAndAs(t *testing.T) {
	base := New(StageTimebase, SeverityRecoverable, "regression").Wrap(ErrTimeRegression)
	if !errors.Is(base, ErrTimeRegression) {
		t.Fatal("errors.Is failed on wrapped cause")
	}
	se := AsStreamError(base)
	if se == nil || se.Stage != StageTimebase || se.Severity != SeverityRecoverable {
		t.Fatalf("unexpected extracted error: %v", se)
	}
	if IsFatal(base) {
		t.Fatal("recoverable error reported as fatal")
	}
	fatal := New(StagePSI, SeverityFatal, "unparseable")
	if !IsFatal(fatal) {
		t.Fatal("fatal error not recognized")
	}
}

func TestAtDoesNotMutateReceiver(t *testing.T) {
	base := New(StageIndex, SeverityInternal, "boom")
	withCtx := base.At(0x100, 10)
	if base.PID != PIDNotApplicable || base.Offset != OffsetUnknown {
		t.Fatal("At must return a copy, not mutate the receiver")
	}
	if withCtx.PID != 0x100 || withCtx.Offset != 10 {
		t.Fatal("context not attached to returned error")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
