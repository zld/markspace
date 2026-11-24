package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestMediaErrorLocationAndExitCodes(t *testing.T) {
	corrupt := NewError(ErrHashMismatch, "vid-1", 42, 1024, "digest differs")
	if corrupt.ExitCode() != 2 {
		t.Errorf("corruption exit = %d, want 2", corrupt.ExitCode())
	}
	if got := corrupt.Error(); got != "hash_mismatch at track=vid-1 frame=42 offset=1024: digest differs" {
		t.Errorf("error text = %q", got)
	}

	for _, et := range []ErrorType{ErrGraphCycle, ErrMissingDep, ErrDuplicateNode, ErrInvalidConfig} {
		if code := NewError(et, "", 0, 0, "").ExitCode(); code != 1 {
			t.Errorf("%s exit = %d, want 1", et, code)
		}
	}
	for _, et := range []ErrorType{ErrMissingFrame, ErrTruncatedFrame, ErrTimestampBack, ErrUnknownColor} {
		if NewError(et, "t", 1, 0, "").ExitCode() != 2 {
			t.Errorf("%s exit = %d, want 2", et, 2)
		}
	}
}

func TestAsMediaErrorThroughWrap(t *testing.T) {
	base := NewError(ErrTruncatedFrame, "a", 3, 512, "short read")
	wrapped := fmt.Errorf("stage failed: %w", base)
	got := AsMediaError(wrapped)
	if got != base {
		t.Fatalf("unwrapped error = %v, want %v", got, base)
	}
	if AsMediaError(errors.New("plain")) != nil {
		t.Fatal("plain error should not classify as MediaError")
	}
}
