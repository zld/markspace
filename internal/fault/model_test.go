package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodes(t *testing.T) {
	corrupt := Corrupt(StageScan, "clip.mp4", 128, "moov", "truncated box header")
	if got := ExitCode(corrupt); got != 2 {
		t.Fatalf("corrupt exit = %d", got)
	}
	ts := Timestamp(StageRepair, "clip.mp4", 64, 1, 90000, "timestamp moves backwards")
	if got := ExitCode(ts); got != 2 {
		t.Fatalf("timestamp exit = %d", got)
	}
	if got := ExitCode(New(KindIO, StageWrite, "disk full")); got != 1 {
		t.Fatalf("io exit = %d", got)
	}
	if got := ExitCode(errors.New("plain")); got != 1 {
		t.Fatalf("plain exit = %d", got)
	}
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("nil exit = %d", got)
	}
}

func TestFaultCarriesContext(t *testing.T) {
	cause := errors.New("EOF")
	f := Corrupt(StageScan, "a.mov", 4096, "trak", "bad length").Wrap(cause)
	if f.Unwrap() != cause {
		t.Fatal("cause not retained")
	}
	msg := f.Error()
	for _, want := range []string{"corrupt/scan", "file=a.mov", "box=trak", "offset=4096", "bad length", "EOF"} {
		if !contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
	wrapped := fmt.Errorf("layer: %w", f)
	got, ok := AsFault(wrapped)
	if !ok || got != f {
		t.Fatal("errors.As did not recover fault")
	}
	if ExitCode(wrapped) != 2 {
		t.Fatal("wrapped fault exit code")
	}
}

func TestUnknownOffsetRendering(t *testing.T) {
	f := New(KindInvariant, StagePipeline, "queue accounting")
	if f.Offset != -1 {
		t.Fatalf("offset = %d", f.Offset)
	}
	if !contains(f.Error(), "offset=unknown") {
		t.Fatalf("message = %q", f.Error())
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
