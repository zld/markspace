package fault

import (
	"errors"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	e := AtLine(StageParse, KindTruncated, "a.srt", 9, "cue has no end timestamp")
	got := e.Error()
	want := "parse/truncated_cue: source=a.srt line=9 cue has no end timestamp"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if e.Stage != StageParse || e.Kind != KindTruncated || e.Line != 9 {
		t.Fatal("structured fields not preserved")
	}
}

func TestAtOffset(t *testing.T) {
	e := AtOffset(StageIndex, KindBounds, "x.mp4", 4096, "stco points past EOF")
	if got := e.Error(); got != "index/bounds_violation: source=x.mp4 stco points past EOF (offset=4096)" {
		t.Fatalf("unexpected: %q", got)
	}
}

type wrapped struct{ err error }

func (w *wrapped) Error() string { return w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

func TestAs(t *testing.T) {
	base := New(StageClock, KindOverflow, "", "tick overflow")
	if As(base) != base {
		t.Fatal("As should return the direct *Error")
	}
	if got := As(&wrapped{err: base}); got != base {
		t.Fatal("As should unwrap one layer")
	}
	if As(errors.New("plain")) != nil {
		t.Fatal("As should return nil for unrelated errors")
	}
	if As(nil) != nil {
		t.Fatal("As(nil) should be nil")
	}
}
