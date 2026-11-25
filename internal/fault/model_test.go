package fault

import (
	"errors"
	"testing"
)

func TestClassifyAndExitCode(t *testing.T) {
	err := InLayer(BoundaryMismatch, "720p", 2, 42, "bad boundary")
	if Classify(err) != BoundaryMismatch {
		t.Fatalf("classify=%q", Classify(err))
	}
	if ExitCode(err) != 2 {
		t.Fatalf("exit=%d want 2", ExitCode(err))
	}

	wrapped := Wrap(DigestMismatch, errors.New("disk"), "resume failed")
	if !errors.Is(wrapped, wrapped.Err) {
		t.Fatal("wrapped cause not reachable")
	}
	if Classify(wrapped) != DigestMismatch || ExitCode(wrapped) != 2 {
		t.Fatalf("digest classification/exit wrong: %q %d", Classify(wrapped), ExitCode(wrapped))
	}

	plain := errors.New("plain")
	if Classify(plain) != Unknown || ExitCode(plain) != 1 {
		t.Fatalf("plain errors should be unknown/1 got %q/%d", Classify(plain), ExitCode(plain))
	}
}
