package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorRendersCodeStageOffset(t *testing.T) {
	err := New(CodeRIFFLength, StageScanned, 42, "bad length")
	want := "SCANNED: E_RIFF_LENGTH at offset 42: bad length"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestErrorWithoutOffsetOmitsIt(t *testing.T) {
	err := New(CodeArgs, StageReceived, -1, "bad flag")
	if got := err.Error(); got != "RECEIVED: E_ARGS: bad flag" {
		t.Fatalf("error = %q", got)
	}
}

func TestWrapKeepsCause(t *testing.T) {
	cause := errors.New("disk gone")
	err := Wrap(CodeIO, StageArchived, 7, cause, "write object")
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not reachable")
	}
	if CodeOf(err) != CodeIO || StageOf(err) != StageArchived || OffsetOf(err) != 7 {
		t.Fatalf("code=%q stage=%q offset=%d", CodeOf(err), StageOf(err), OffsetOf(err))
	}
}

func TestCodeOfUnwrapsNestedFaults(t *testing.T) {
	inner := New(CodeFrameAlign, StageScanned, 12, "off grid")
	outer := fmt.Errorf("pipeline: %w", inner)
	if CodeOf(outer) != CodeFrameAlign {
		t.Fatalf("code = %q", CodeOf(outer))
	}
	if StageOf(outer) != StageScanned {
		t.Fatalf("stage = %q", StageOf(outer))
	}
}

func TestCodeOfPlainErrorIsEmpty(t *testing.T) {
	plain := errors.New("not a fault")
	if CodeOf(plain) != "" || StageOf(plain) != "" || OffsetOf(plain) != -1 {
		t.Fatal("plain errors must not masquerade as faults")
	}
}

func TestStageOrderingIsStable(t *testing.T) {
	order := []Stage{StageReceived, StageScanned, StageFingerprinted, StageNormalized, StageArchived}
	for i := 1; i < len(order); i++ {
		if order[i-1].Order() >= order[i].Order() {
			t.Fatalf("%s does not sort before %s", order[i-1], order[i])
		}
	}
	if Stage("BOGUS").Order() <= StageArchived.Order() {
		t.Fatal("unknown stages must sort last")
	}
}
