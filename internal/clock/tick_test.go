package clock

import "testing"

func TestTraceErrorAnnotations(t *testing.T) {
	base := NewError(CodeIllegalVINT, 7, "/Segment", "bad width")
	if got := base.AtOffset(42); got.Offset != 42 || got.Code != CodeIllegalVINT {
		t.Fatalf("AtOffset lost fields: %+v", got)
	}
	if got := base.AtPath("/EBML"); got.Path != "/EBML" || got.Offset != 7 {
		t.Fatalf("AtPath lost fields: %+v", got)
	}
	if s := base.Error(); s == "" {
		t.Fatal("Error() returned empty string")
	}
	if AsTraceError(nil) != nil {
		t.Fatal("AsTraceError(nil) must be nil")
	}
	if AsTraceError(base) != base {
		t.Fatal("AsTraceError must return same pointer")
	}
}

func TestTickHelpers(t *testing.T) {
	if Abs(Tick(3), Tick(10)) != Tick(7) {
		t.Fatal("Abs wrong")
	}
	if Max(Tick(-1), Tick(4)) != Tick(4) || Min(Tick(-1), Tick(4)) != Tick(-1) {
		t.Fatal("Max/Min wrong")
	}
	if Tick(123).Nanoseconds() != 123 {
		t.Fatal("Nanoseconds wrong")
	}
}
