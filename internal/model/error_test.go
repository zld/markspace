package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorStringCarriesSourceAndCode(t *testing.T) {
	e := Newf(CodeSubtitleTiming, Source{Name: "captions.srt", Line: 7}, "end %d precedes start %d", 100, 200)
	got := e.Error()
	for _, want := range []string{"subtitle_timing", "captions.srt", "line=7"} {
		if !contains(got, want) {
			t.Errorf("error %q lacks %q", got, want)
		}
	}
	if CodeOf(e) != CodeSubtitleTiming {
		t.Fatalf("code %s", CodeOf(e))
	}
	if _, ok := AsError(fmt.Errorf("wrapped: %w", e)); !ok {
		t.Fatal("errors.As lost the typed error")
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

func TestAtLineAndOffsetCopy(t *testing.T) {
	base := New(CodeIO, Source{Name: "x", Offset: 5}, "boom", nil)
	line := base.AtLine(3)
	if base.Source.Line != 0 || line.Source.Line != 3 || line.Source.Offset != 5 {
		t.Fatalf("copy mutated original: %+v %+v", base.Source, line.Source)
	}
	off := base.AtOffset(99)
	if off.Source.Offset != 99 || base.Source.Offset != 5 {
		t.Fatalf("offset copy leaked: %+v", base.Source)
	}
}

func TestUnwrapSentinel(t *testing.T) {
	sentinel := errors.New("disk gone")
	e := New(CodeIO, Source{Name: "wal"}, "append failed", sentinel)
	if !errors.Is(e, sentinel) {
		t.Fatal("sentinel not reachable")
	}
}

func TestSortErrorsDeterministic(t *testing.T) {
	mk := func(name string, line int, code Code) *Error {
		return New(code, Source{Name: name, Line: line}, "", nil)
	}
	errs := []*Error{
		mk("b", 1, CodeIO), mk("a", 2, CodeIO), mk("a", 1, CodeSubtitleSyntax),
		mk("a", 1, CodeIO), mk("a", 1, CodeIO),
	}
	SortErrors(errs)
	want := []string{"a/1/io", "a/1/io", "a/1/subtitle_syntax", "a/2/io"}
	// Codes sort lexicographically, so io precedes subtitle_syntax.
	for i := 0; i < len(want); i++ {
		got := fmt.Sprintf("%s/%d/%s", errs[i].Source.Name, errs[i].Source.Line, errs[i].Code)
		if got != want[i] {
			t.Fatalf("position %d = %s, want %s", i, got, want[i])
		}
	}
}
