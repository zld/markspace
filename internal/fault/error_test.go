package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorRendering(t *testing.T) {
	e := New(StageChecksum, KindChecksum, "bad digest").At(42, 3).WithTrack(2)
	got := e.Error()
	for _, want := range []string{"stage=checksum", "kind=checksum_mismatch", "track=2", "offset=42", "record=3", "bad digest"} {
		if !contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestAsAndWrap(t *testing.T) {
	base := errors.New("io failure")
	e := Wrap(StageParse, KindIO, base, "read header")
	if !errors.Is(e, base) {
		t.Fatal("chain does not reach cause")
	}
	got, ok := As(fmt.Errorf("outer: %w", e))
	if !ok || got.Kind != KindIO || !got.Recoverable {
		t.Fatalf("As = %+v ok=%v", got, ok)
	}
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("plain error reported as fault")
	}
}

func TestListHelpers(t *testing.T) {
	var l List
	l.Add(New(StageParse, KindChecksum, "c").At(90, 1))
	l.Add(New(StageParse, KindTruncated, "t").At(10, 0))
	l.Add(errors.New("unclassified"))
	if l.Len() != 3 {
		t.Fatalf("len = %d", l.Len())
	}
	if l.CountKind(KindChecksum) != 1 {
		t.Fatalf("checksum count = %d", l.CountKind(KindChecksum))
	}
	if off, ok := l.FirstOffset(); !ok || off != 10 {
		t.Fatalf("first offset = %d ok=%v", off, ok)
	}
	sorted := l.Sorted()
	if sorted[0].Offset != 10 || sorted[1].Offset != 90 {
		t.Fatalf("sorted = %+v", sorted)
	}
	if l.Summary() == "" {
		t.Fatal("empty summary")
	}
	if l[2].Kind != KindIO {
		t.Fatalf("plain error wrapped as %s", l[2].Kind)
	}
}

func TestListFirstOffsetWithoutOffsets(t *testing.T) {
	var l List
	l.Add(New(StageJitter, KindDuplicate, "dup"))
	if _, ok := l.FirstOffset(); ok {
		t.Fatal("offset reported for offset-less fault")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
