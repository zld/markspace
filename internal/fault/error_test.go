package fault

import (
	"errors"
	"testing"
)

func TestErrorFormatAndAccessors(t *testing.T) {
	e := New(CatHeader, "wav.feed", true, "bad channel count").WithOffset(42).WithTrack(2)
	if e.Offset != 42 || e.Track != 2 || !e.Recoverable {
		t.Fatalf("accessors lost: %+v", e)
	}
	if s := e.Error(); s == "" || s != "header_mismatch [wav.feed] offset=42 track=2: bad channel count" {
		t.Fatalf("unexpected error string: %q", s)
	}
	base := errors.New("disk full")
	w := Wrap(CatWrite, "mp4.write", 7, 0, false, base)
	if !errors.Is(w, base) {
		t.Fatal("wrapped cause must be reachable via errors.Is")
	}
	if got, ok := As(w); !ok || got.Category != CatWrite {
		t.Fatal("As must recover *fault.Error from wrapped chain")
	}
	if _, ok := As(base); ok {
		t.Fatal("plain error must not be mistaken for *fault.Error")
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		cat  Category
		want int
	}{
		{CatTruncated, 2},
		{CatRTPGap, 2},
		{CatCheckpoint, 2},
		{CatHash, 2},
		{CatMapping, 2},
		{CatWrite, 3},
		{CatCanceled, 3},
	}
	for _, c := range cases {
		if got := ExitCode(New(c.cat, "op", true, "x")); got != c.want {
			t.Errorf("category %s: got %d want %d", c.cat, got, c.want)
		}
	}
	if ExitCode(nil) != 0 {
		t.Error("nil error must map to exit code 0")
	}
	if ExitCode(errors.New("mystery")) != 3 {
		t.Error("unknown errors must be treated as internal failures")
	}
}
