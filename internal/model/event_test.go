package model

import (
	"errors"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	e := NewError(ErrTruncated, StageDemux, "mp4:1", 4096, "box body short")
	want := "demux/truncated track=\"mp4:1\" offset=4096: box body short"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestAsError(t *testing.T) {
	if AsError(nil, StageDemux) != nil {
		t.Fatal("nil error should stay nil")
	}
	me := NewError(ErrMapping, StageMap, "", -1, "dup")
	if AsError(me, StageDemux) != me {
		t.Fatal("typed error should pass through unchanged")
	}
	plain := errors.New("boom")
	got := AsError(plain, StageRemux)
	if got.Category != ErrIO || got.Stage != StageRemux || got.Offset != -1 {
		t.Fatalf("unexpected wrap: %+v", got)
	}
}
