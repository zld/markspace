package diagnostic

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorContainsLocationAndUnwrap(t *testing.T) {
	root := errors.New("boom")
	err := Wrap(CodeTruncated, PhaseSubtitle, 7, 42, root, "cue %d incomplete", 3)

	if !errors.Is(err, root) {
		t.Fatal("wrapped root is not reachable")
	}
	if !HasCode(err, CodeTruncated) {
		t.Fatal("HasCode did not find structured code")
	}
	for _, want := range []string{"TRUNCATED_INPUT", "phase=subtitle", "sample=7", "offset=42", "cue 3", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Error() %q does not contain %q", err.Error(), want)
		}
	}
}

func TestFromMiss(t *testing.T) {
	if _, ok := From(errors.New("plain")); ok {
		t.Fatal("plain error unexpectedly converted")
	}
	if HasCode(nil, CodeCanceled) {
		t.Fatal("nil error must not match a code")
	}
}
