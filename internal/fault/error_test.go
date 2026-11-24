package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorCarriesContext(t *testing.T) {
	err := New(StageSampleTable, "truncated trun").
		At("/in/seg-1.m4s", 4096).
		For("v1", 3).
		Wrap(fmt.Errorf("unexpected EOF"))
	s := err.Error()
	for _, want := range []string{"parse-sample-table", "v1", "seg-1.m4s", "4096", "entry=3", "unexpected EOF"} {
		if !contains(s, want) {
			t.Fatalf("error string %q missing %q", s, want)
		}
	}
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatal("errors.As failed")
	}
	if fe.Offset != 4096 || fe.Entry != 3 || fe.Track != "v1" {
		t.Fatalf("bad fields: %+v", fe)
	}
}

func TestImmutabilityOfBuilders(t *testing.T) {
	base := New(StageManifest, "bad")
	derived := base.At("/x", 1)
	if base.Offset != -1 || derived.Offset != 1 {
		t.Fatal("builder mutated the original")
	}
}

func TestExitCodes(t *testing.T) {
	if got := ExitCode(nil); got != ExitOK {
		t.Fatalf("nil -> %d", got)
	}
	if got := ExitCode(New(StageBoxHeader, "x")); got != ExitValidation {
		t.Fatalf("parse -> %d", got)
	}
	if got := ExitCode(New(StageJournal, "x")); got != ExitCommitPhase {
		t.Fatalf("journal -> %d", got)
	}
	if got := ExitCode(New(StageCommit, "x")); got != ExitCommitPhase {
		t.Fatalf("commit -> %d", got)
	}
	if got := ExitCode(errors.New("plain")); got != ExitValidation {
		t.Fatalf("plain -> %d", got)
	}
	// Wrapped faults keep their exit code.
	wrapped := fmt.Errorf("outer: %w", New(StageCommit, "x"))
	if got := ExitCode(wrapped); got != ExitCommitPhase {
		t.Fatalf("wrapped commit -> %d", got)
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
