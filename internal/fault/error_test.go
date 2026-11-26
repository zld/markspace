package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorFormatAndExitStatus(t *testing.T) {
	err := New(TruncatedBox, StageScan, "fixture.mp4", 16, -1, "box mdat ends past EOF")
	if got := ExitStatus(err); got != 2 {
		t.Fatalf("exit status = %d, want 2", got)
	}
	msg := err.Error()
	for _, want := range []string{"TRUNCATED_BOX", "offset=16", "raw_offset=16", "stage=scan", "fixture.mp4"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}

	wrapped := Wrap(errors.New("read failed"), IO, StageRender, "x.mp4", 42, 7)
	if wrapped.Code != IO || wrapped.Offset != 42 {
		t.Fatalf("unexpected wrap: %#v", wrapped)
	}
	if ExitStatus(New(InjectedExit, StageJournal, "", -1, -1, "stop")) != 75 {
		t.Fatal("injected exit must map to 75")
	}
}

func TestWrapPreservesStructuredCause(t *testing.T) {
	original := New(TimeGap, StageTimeline, "events.rtp", 88, 90000, "empty clip")
	wrapped := Wrap(original, Render, StagePipeline, "other", 1, 2)
	if wrapped != original {
		t.Fatal("Wrap must preserve an existing *Error")
	}
	if wrapped.Path != "events.rtp" || wrapped.Offset != 88 {
		t.Fatalf("original metadata overwritten: %#v", wrapped)
	}
}
