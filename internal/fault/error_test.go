package fault

import (
	"errors"
	"testing"
)

func TestNewAndError(t *testing.T) {
	f := New(ErrTruncated, "subtitle", 42, "box ends at %d", 99)
	if f.Code != ErrTruncated || f.Track != "subtitle" || f.Offset != 42 {
		t.Fatalf("unexpected fault fields: %+v", f)
	}
	if got := f.Error(); got != `truncated(1001) track="subtitle" offset=42: box ends at 99` {
		t.Fatalf("unexpected error string: %s", got)
	}

	g := New(ErrCanceled, "audio", -1, "ctx done")
	if got := g.Error(); got != `canceled(4003) track="audio": ctx done` {
		t.Fatalf("unexpected error string without offset: %s", got)
	}
}

func TestCodeStringStable(t *testing.T) {
	cases := map[Code]string{
		OK:                   "ok",
		ErrTruncated:         "truncated",
		ErrOutOfBounds:       "out_of_bounds",
		ErrIllegalTimebase:   "illegal_timebase",
		ErrSampleRange:       "sample_range",
		ErrChunkBoundary:     "chunk_boundary",
		ErrContainerDecl:     "container_declaration",
		ErrWrapAmbiguous:     "wrap_ambiguous",
		ErrDriftTooLarge:     "drift_too_large",
		ErrPacketMalformed:   "packet_malformed",
		ErrPacketUnrecovered: "packet_unrecovered",
		ErrLimitExceeded:     "limit_exceeded",
		ErrOverflow:          "arithmetic_overflow",
		ErrCanceled:          "canceled",
		Code(99999):          "unknown_99999",
	}
	for code, want := range cases {
		if got := code.String(); got != want {
			t.Errorf("code %d: got %q want %q", code, got, want)
		}
	}
}

func TestAsFault(t *testing.T) {
	orig := New(ErrOverflow, "video", 7, "bad add")
	wrapped := errors.New("wrapper: " + orig.Error())
	if _, ok := AsFault(wrapped); ok {
		t.Fatal("plain string error must not unwrap to Fault")
	}
	var err error = orig
	f, ok := AsFault(err)
	if !ok || f != orig {
		t.Fatalf("AsFault failed: ok=%v f=%v", ok, f)
	}
}
