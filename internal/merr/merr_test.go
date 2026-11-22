package merr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorCarriesEverySpanField(t *testing.T) {
	e := New(StageDemux, "gap", Span{Stream: 2, Seq: 41, Offset: 128, Length: 16, TrackID: 7},
		true, "missing %d packets", 3)
	got := e.Error()
	for _, want := range []string{
		"demux", "gap", "stream=2", "seq=41", "off=128", "len=16", "track=7",
		"missing 3 packets", "recoverable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q missing %q", got, want)
		}
	}
	if !e.IsRecovered() {
		t.Fatal("recoverable flag lost")
	}
}

func TestFatalErrorIsMarked(t *testing.T) {
	e := New(StageRemux, "bad", NoSpan(0), false, "hard stop")
	if e.IsRecovered() {
		t.Fatal("fatal error reported as recoverable")
	}
	if !strings.Contains(e.Error(), "fatal") {
		t.Fatalf("fatal marker missing: %s", e.Error())
	}
}

func TestNoSpanUsesSentinels(t *testing.T) {
	s := NoSpan(3)
	if s.Stream != 3 || s.Seq != -1 || s.Offset != -1 {
		t.Fatalf("span %+v", s)
	}
	sq := SeqSpan(1, 0)
	if sq.Seq != 0 || sq.Offset != -1 {
		t.Fatalf("seq span %+v", sq)
	}
	off := OffsetSpan(0, 10, 5)
	if off.Offset != 10 || off.Length != 5 || off.Seq != -1 {
		t.Fatalf("offset span %+v", off)
	}
}

func TestWrapAndUnwrap(t *testing.T) {
	root := errors.New("underlying")
	e := Wrap(StageIndex, "decode", NoSpan(0), false, root, "cannot read index")
	if !errors.Is(e, root) {
		t.Fatal("wrapped cause is not reachable")
	}
	if e.Unwrap() != root {
		t.Fatal("Unwrap returned the wrong error")
	}
}

func TestAsErrorIgnoresForeignErrors(t *testing.T) {
	if AsError(errors.New("plain")) != nil {
		t.Fatal("foreign error must not be coerced")
	}
	if AsError(nil) != nil {
		t.Fatal("nil must map to nil")
	}
	inner := New(StageCodec, "x", NoSpan(0), true, "inner")
	wrapped := fmt.Errorf("context: %w", inner)
	if got := AsError(wrapped); got == nil || got.Code != "x" {
		t.Fatal("AsError failed through fmt wrapping")
	}
}

func TestNilReceiverIsSafe(t *testing.T) {
	var e *Error
	if e.Error() != "<nil>" {
		t.Fatalf("nil error text %q", e.Error())
	}
	if e.Unwrap() != nil || e.IsRecovered() {
		t.Fatal("nil receiver misbehaved")
	}
}

func TestErrorTextIsDeterministic(t *testing.T) {
	mk := func() *Error {
		return New(StageTransport, "short_header", OffsetSpan(1, 0, 8), false, "datagram too short")
	}
	first := mk().Error()
	for i := 0; i < 50; i++ {
		if mk().Error() != first {
			t.Fatal("error text is not stable")
		}
	}
}
