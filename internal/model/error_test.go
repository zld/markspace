package model

import (
	"strings"
	"testing"

	"github.com/local/medialab/internal/timeline"
)

func TestErrorBookLocateAndCount(t *testing.T) {
	var b ErrorBook
	b.Add(ErrorRecord{Code: CodeTruncated, Offset: 100, Track: 1,
		Window: timeline.Window{Start: 0, End: 1000}, Detail: "mdat cut"})
	b.Add(ErrorRecord{Code: CodeInvalidTimestamp, Offset: 200, Track: 1,
		Window: timeline.Window{Start: 500, End: 1500}, Detail: "negative dt"})
	b.Add(ErrorRecord{Code: CodeTruncated, Offset: 300, Track: 1,
		Window: timeline.Window{Start: 2000, End: 3000}, Detail: "tail cut"})

	if len(b.All()) != 3 {
		t.Fatalf("records = %d, want 3", len(b.All()))
	}
	if !b.Has(CodeTruncated) || b.Has(CodeMissingIndex) {
		t.Fatal("Has predicate wrong")
	}
	counts := b.CountByCode()
	if counts[CodeTruncated] != 2 || counts[CodeInvalidTimestamp] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestMergeWindowsCollapsesRanges(t *testing.T) {
	var b ErrorBook
	// overlapping pair must collapse; adjacent pair must collapse;
	// distant range stays separate.
	b.Add(ErrorRecord{Code: CodeIncompleteFrame, Window: timeline.Window{Start: 0, End: 10}})
	b.Add(ErrorRecord{Code: CodeTruncated, Window: timeline.Window{Start: 5, End: 20}})
	b.Add(ErrorRecord{Code: CodeTruncated, Window: timeline.Window{Start: 20, End: 30}})
	b.Add(ErrorRecord{Code: CodeTruncated, Window: timeline.Window{Start: 100, End: 110}})
	// non-matching code must be ignored
	b.Add(ErrorRecord{Code: CodeRTPLost, Window: timeline.Window{Start: 0, End: 110}})

	got := b.MergeWindows(CodeTruncated, CodeIncompleteFrame)
	want := []timeline.Window{
		{Start: 0, End: 30},
		{Start: 100, End: 110},
	}
	if len(got) != len(want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("window %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestErrorRecordString(t *testing.T) {
	r := ErrorRecord{Code: CodeMissingIndex, Offset: 42, Track: 2,
		Window: timeline.Window{Start: timeline.MasterHz, End: 2 * timeline.MasterHz},
		Detail: "stss absent"}
	s := r.String()
	for _, frag := range []string{string(CodeMissingIndex), "track=2", "offset=42", "[1000ms,2000ms)", "stss absent"} {
		if !strings.Contains(s, frag) {
			t.Fatalf("record string %q missing %q", s, frag)
		}
	}
}
