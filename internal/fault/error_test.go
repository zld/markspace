package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	e := New(StageFrame, "checksum", ErrChecksum).
		AtTrack("video/0").AtOffset(128).AtFrame(7)
	s := e.Error()
	for _, want := range []string{"frame", "checksum", "video/0", "frame=7", "offset=128"} {
		if !strings.Contains(s, want) {
			t.Fatalf("error %q missing %q", s, want)
		}
	}
	if !errors.Is(e, ErrChecksum) {
		t.Fatal("errors.Is must reach the wrapped cause")
	}
}

func TestAsError(t *testing.T) {
	if AsError(nil) != nil {
		t.Fatal("nil must map to nil")
	}
	plain := errors.New("boom")
	fe := AsError(plain)
	if fe.Stage != StagePipeline || !errors.Is(fe, plain) {
		t.Fatalf("unexpected wrap: %+v", fe)
	}
	orig := New(StageSilence, "reorder", ErrUnknownWindow)
	if AsError(orig) != orig {
		t.Fatal("existing fault must be returned as-is")
	}
}

func TestUnknownMarkers(t *testing.T) {
	e := New(StageClock, "tick", errors.New("invalid ticks per second"))
	if e.Offset != -1 || e.Frame != -1 {
		t.Fatal("default markers must be -1")
	}
	if !strings.Contains(e.Error(), "clock") {
		t.Fatalf("got %q", e.Error())
	}
}
