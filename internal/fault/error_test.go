package fault

import (
	"errors"
	"testing"
)

func TestTruncatedErrorOffsetsAndExitCode(t *testing.T) {
	err := Truncated("mp4", 40, 53, "moof box")
	if err.FirstUnreadable != 53 {
		t.Fatalf("FirstUnreadable=%d want 53", err.FirstUnreadable)
	}
	wrapped := Wrap(KindInvalid, err, "scan failed")
	got, ok := As(wrapped)
	if !ok || got.FirstUnreadable != 53 {
		t.Fatalf("As()=%#v,%v want offset 53", got, ok)
	}
	if code := ExitCode(wrapped); code != 2 {
		t.Fatalf("ExitCode(truncated)=%d want 2", code)
	}
	if code := ExitCode(New(KindArgument, "bad")); code != 1 {
		t.Fatalf("ExitCode(argument)=%d want 1", code)
	}
	if code := ExitCode(New(KindDependency, "blocked")); code != 3 {
		t.Fatalf("ExitCode(dependency)=%d want 3", code)
	}
}

func TestIsolatedCorruptionExitCode(t *testing.T) {
	if code := ExitCode(errors.Join(ErrIsolatedCorruption, nil)); code != 2 {
		t.Fatalf("ExitCode(ErrIsolatedCorruption)=%d want 2", code)
	}
}
