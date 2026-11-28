package errors

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorCarriesOffsetTickAndState(t *testing.T) {
	base := errors.New("eof from library")
	err := New(CodeTruncatedBox, "mdat ends early").
		AtOffset(4096).
		AtTick(-120, 48000).
		WithState(StateFatal).
		Wrap(base)

	got, ok := As(err)
	if !ok {
		t.Fatal("As must find *Error through chain")
	}
	if got.Code != CodeTruncatedBox || got.Offset != 4096 || got.RawTimestamp != -120 ||
		got.RawTimescale != 48000 || got.State != StateFatal {
		t.Fatalf("fields lost: %+v", got)
	}
	if !errors.Is(err, base) {
		t.Fatal("Unwrap must expose cause")
	}
	s := err.Error()
	for _, want := range []string{CodeTruncatedBox, "offset=4096", "tick=-120/48000Hz", "mdat ends early"} {
		if !strings.Contains(s, want) {
			t.Fatalf("Error() %q missing %q", s, want)
		}
	}
}

func TestAsOnNilAndPlainError(t *testing.T) {
	if _, ok := As(nil); ok {
		t.Fatal("nil error must not match")
	}
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("plain error must not match")
	}
}

func TestCodesAreStableStrings(t *testing.T) {
	// Guard against accidental renames of the public vocabulary.
	want := map[string]string{
		CodeTruncatedBox:     "MP4_TRUNCATED_BOX",
		CodeMissingTrack:     "MAP_MISSING_TRACK",
		CodeRTPConflict:      "RTP_CONTENT_CONFLICT",
		CodeBoundaryMismatch: "FANIN_BOUNDARY_MISMATCH",
		CodeQualityGate:      "POLICY_QUALITY_GATE",
	}
	for code, literal := range want {
		if code != literal {
			t.Fatalf("code drift: %s != %s", code, literal)
		}
	}
}
