package continuity

import (
	"errors"
	"testing"
)

func TestErrorFormattingAndLocation(t *testing.T) {
	loc := Location{File: "main/index.m3u8", Line: 7, Offset: -1, Sample: -1}
	err := NewError(KindParse, loc, "missing EXTINF")
	want := "parse: missing EXTINF [main/index.m3u8:7]"
	if err.Error() != want {
		t.Fatalf("Error()=%q want %q", err.Error(), want)
	}
	if FailureLocation(err) != "main/index.m3u8:7" {
		t.Fatalf("FailureLocation=%q", FailureLocation(err))
	}
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode=%d", ExitCode(err))
	}
}

func TestUsageExitCode(t *testing.T) {
	err := Usagef("bad --late-ms")
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode=%d", ExitCode(err))
	}
	if e, ok := AsError(err); !ok || e.Kind != KindUsage {
		t.Fatalf("not a usage fault: %#v", err)
	}
	if ExitCode(nil) != 0 {
		t.Fatal("nil error must map to 0")
	}
}

func TestSegmentLocation(t *testing.T) {
	loc := atSegmentSample("seg1.m4s", "main/seg1.m4s", 128, 3)
	if got := loc.String(); got != "main/seg1.m4s@128#3" {
		t.Fatalf("String()=%q", got)
	}
	err := WrapError(errors.New("short mdat"), KindTruncated, loc, "truncated box")
	if !IsKind(err, KindTruncated) {
		t.Fatal("IsKind failed")
	}
	if got := FailureLocation(err); got != "main/seg1.m4s@128#3" {
		t.Fatalf("FailureLocation=%q", got)
	}
	if !errors.Is(err, errors.Unwrap(err)) {
		t.Fatal("unreachable")
	}
}

func TestExitCodesByKind(t *testing.T) {
	for _, kind := range []Kind{
		KindParse, KindPathEscape, KindTruncated, KindUnsupported,
		KindBadReference, KindMapping, KindLate, KindQueueLimit, KindCheckpoint, KindMux,
	} {
		if code := ExitCode(NewError(kind, Location{Offset: -1, Sample: -1}, "x")); code != 2 {
			t.Errorf("kind %s exit=%d want 2", kind, code)
		}
	}
}

func TestNoLocationFailure(t *testing.T) {
	err := errors.New("boom")
	if FailureLocation(err) != "none" {
		t.Fatalf("FailureLocation=%q", FailureLocation(err))
	}
	if ExitCode(err) != 2 {
		t.Fatalf("unclassified failures must be rejected, got %d", ExitCode(err))
	}
}
