package media

import (
	"errors"
	"fmt"
	"testing"
)

func TestStructuredErrorCarriesLocation(t *testing.T) {
	cause := fmt.Errorf("short box header")
	e := NewError(CodeTruncated, PhaseContainer, "truncated at box header").
		AtOffset(4096).OnSegment(3, "seg-3.m4s").OnNode("edge-a").OnTrack(2).Because(cause)
	if e.Offset != 4096 || e.Segment != 3 || e.Node != "edge-a" || e.Track != 2 {
		t.Fatalf("missing location: %+v", e)
	}
	want := "container/truncated node=edge-a segment=3(seg-3.m4s) track=2 offset=4096: truncated at box header: short box header"
	if e.Error() != want {
		t.Fatalf("got %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Fatal("cause must unwrap")
	}
	if !HasCode(e, CodeTruncated) {
		t.Fatal("code lookup failed")
	}
}

func TestAsErrorFindsWrapped(t *testing.T) {
	inner := NewError(CodeCachePoison, PhaseCache, "source mismatch")
	outer := fmt.Errorf("restore: %w", inner)
	got, ok := AsError(outer)
	if !ok || got.Code != CodeCachePoison {
		t.Fatalf("got %v ok=%v", got, ok)
	}
	if HasCode(nil, CodeInput) {
		t.Fatal("nil must not match")
	}
}
