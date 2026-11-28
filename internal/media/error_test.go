package media

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorMessageCarriesLocation(t *testing.T) {
	err := New(StageParse, "hls", "invalid EXTINF").
		WithPath("cases/broken.m3u8").WithLine(7).WithSegment(2)
	msg := err.Error()
	for _, want := range []string{"parse/hls", "cases/broken.m3u8:7", "[segment 2]", "invalid EXTINF"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
}

func TestErrorMessageWithOffset(t *testing.T) {
	err := Wrap(StageParse, "mp4", errors.New("unexpected EOF"), "truncated box").
		WithPath("seg.m4s").WithOffset(4096)
	msg := err.Error()
	if !strings.Contains(msg, "seg.m4s@4096") {
		t.Fatalf("offset missing: %q", msg)
	}
	if !strings.Contains(msg, "unexpected EOF") {
		t.Fatalf("wrapped cause missing: %q", msg)
	}
}

func TestExitCodeMapping(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("nil = %d", got)
	}
	if got := ExitCode(Usage("bad flag")); got != 1 {
		t.Fatalf("usage = %d", got)
	}
	if got := ExitCode(New(StageParse, "hls", "bad")); got != 2 {
		t.Fatalf("parse = %d", got)
	}
	if got := ExitCode(errors.New("plain")); got != 2 {
		t.Fatalf("plain = %d", got)
	}
}

func TestStageOf(t *testing.T) {
	if s, ok := StageOf(New(StageExecute, "stream", "x")); !ok || s != StageExecute {
		t.Fatalf("StageOf = %v %v", s, ok)
	}
	if _, ok := StageOf(errors.New("plain")); ok {
		t.Fatal("plain error should have no stage")
	}
}
