package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	e := New(CodeTruncatedBox, "box %s ends early", "mdat")
	if e.Error() != "truncated_box: box mdat ends early" {
		t.Fatalf("unexpected message: %s", e.Error())
	}
	if e.Recoverable {
		t.Fatal("New must produce a non-recoverable error")
	}
	e2 := e.WithInput("rep0/frag-003.m4s")
	if e2.Input != "rep0/frag-003.m4s" || e2 != e {
		t.Fatal("WithInput should mutate and return the same error")
	}
	if got := e2.Error(); got != "truncated_box: box mdat ends early (input=rep0/frag-003.m4s)" {
		t.Fatalf("unexpected contextual message: %s", got)
	}
}

func TestRecoverableAndDetail(t *testing.T) {
	e := Recoverable(CodeTFDTRegression, "resume from last checkpoint", "tfdt went backwards").
		WithDetail("previous", uint64(9000)).
		WithDetail("observed", uint64(1000))
	if !e.Recoverable || e.Hint == "" {
		t.Fatal("recoverable error must carry a hint")
	}
	if e.Detail["previous"] != uint64(9000) || e.Detail["observed"] != uint64(1000) {
		t.Fatalf("unexpected detail map: %#v", e.Detail)
	}
}

func TestCodeOfAndIs(t *testing.T) {
	base := New(CodeSequenceGap, "missing 3")
	wrapped := fmt.Errorf("stage failed: %w", base)
	if !Is(wrapped, CodeSequenceGap) {
		t.Error("Is should traverse wrapped errors")
	}
	if CodeOf(wrapped) != CodeSequenceGap {
		t.Error("CodeOf should traverse wrapped errors")
	}
	if Is(errors.New("plain"), CodeManifest) {
		t.Error("plain errors have no fault code")
	}
	if CodeOf(nil) != "" {
		t.Error("nil error must yield empty code")
	}
}
