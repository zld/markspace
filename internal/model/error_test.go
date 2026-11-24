package model

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	e := NewError(CodeManifestMissing, Loc{
		File: "manifest.json", TrackID: 3, Field: "duration_ticks",
		HasByteOffset: true, ByteOffset: 42,
	}, "field is missing")
	got := e.Error()
	for _, want := range []string{
		CodeManifestMissing, "file=manifest.json", "track=3",
		"field=duration_ticks", "offset=42", "field is missing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q missing %q", got, want)
		}
	}
}

func TestErrorByteOffsetZeroIsReported(t *testing.T) {
	e := NewError(CodeMP4Truncated, Loc{File: "x.mp4", HasByteOffset: true, ByteOffset: 0}, "bad header")
	if !strings.Contains(e.Error(), "offset=0") {
		t.Errorf("offset zero must be reported, got %q", e.Error())
	}
}

func TestErrorListOrderAndUnwrap(t *testing.T) {
	var l ErrorList
	l.Add(NewError(CodeManifestSyntax, Loc{Field: "a"}, "first"))
	l.Add(nil)
	l.Add(NewError(CodeManifestInvalid, Loc{Field: "b"}, "second"))
	if l.Count() != 2 {
		t.Fatalf("Count = %d, want 2", l.Count())
	}
	text := l.Error()
	if strings.Index(text, "first") > strings.Index(text, "second") {
		t.Errorf("error order changed: %q", text)
	}
	var target *Error
	if !errors.As(l, &target) {
		t.Fatal("ErrorList should unwrap to *Error")
	}
	if target.Code != CodeManifestSyntax {
		t.Errorf("first unwrapped code = %s", target.Code)
	}
}

func TestErrorWrap(t *testing.T) {
	base := errors.New("disk full")
	e := NewError(CodeCheckpointCorrupt, Loc{File: "ck.jsonl"}, "write").Wrap(base)
	if !errors.Is(e, base) {
		t.Error("wrapped error is not reachable")
	}
	if !strings.Contains(e.Error(), "disk full") {
		t.Errorf("wrapped message missing: %q", e.Error())
	}
}
