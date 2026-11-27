package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorStringContainsLocationOnly(t *testing.T) {
	err := New(KindTruncated, "rtcp sender report truncated").
		At("ingest").
		WithTrack("audio-main").
		WithVersion("v2").
		WithSSRC(0xdeadbeef).
		WithSeq(70000).
		WithOffset(120).
		WithIndex(3)

	got := err.Error()
	for _, want := range []string{
		"truncated",
		"stage=ingest",
		"track=audio-main",
		"version=v2",
		"ssrc=deadbeef",
		"seq=70000",
		"input_index=3",
		"byte_offset=120",
		"rtcp sender report truncated",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error string %q missing %q", got, want)
		}
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"argument", New(KindArgument, "bad flag"), 2},
		{"input", New(KindInput, "bad packet"), 1},
		{"merge", New(KindMerge, "conflict"), 1},
		{"wrapped argument", errors.Join(New(KindInput, "x"), New(KindArgument, "nested")), 2},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Fatalf("%s: ExitCode=%d want %d", tc.name, got, tc.want)
		}
	}
}

func TestCausePreservedForErrorsIs(t *testing.T) {
	base := errors.New("underlying")
	err := New(KindInput, "parse").Wrap(base)
	if !errors.Is(err, base) {
		t.Fatal("wrapped cause not reachable through errors.Is")
	}
	if IsKind(err, KindInput) != true || IsKind(err, KindMerge) != false {
		t.Fatal("kind classification wrong")
	}
}

func TestNilContextFieldsOmitted(t *testing.T) {
	got := New(KindInput, "bare").Error()
	if strings.Contains(got, "ssrc=") || strings.Contains(got, "seq=") || strings.Contains(got, "byte_offset=") {
		t.Fatalf("unset fields should be omitted: %q", got)
	}
}
