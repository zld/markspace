package fault

import (
	"strings"
	"testing"
)

func TestLineIncludesAllRequiredFields(t *testing.T) {
	e := New(TruncatedBox, 4096, "mdat ends early").WithTrack("video", 7).WithRange(100, 200).WithRecoverable(false)
	line := e.Line()
	for _, want := range []string{
		"error_code=truncated_box", "offset_bytes=4096", "track_id=7",
		"start_ns=100", "end_ns=200", "recoverable=false",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
}

func TestLineWithoutRangeIsExplicit(t *testing.T) {
	line := New(MissingIndex, 12, "no index").Line()
	if !strings.Contains(line, "start_ns=-1 end_ns=-1") {
		t.Fatalf("line = %q", line)
	}
}

func TestListSortDedupAndCodes(t *testing.T) {
	l := List{
		New(MissingIndex, 20, "b"),
		New(TruncatedBox, 10, "a"),
		New(TruncatedBox, 10, "a"),
	}
	l = l.Dedup()
	if len(l) != 2 {
		t.Fatalf("dedup size = %d", len(l))
	}
	if l[0].Offset != 10 || l[1].Offset != 20 {
		t.Fatalf("sort order: %v", l)
	}
	if l.ByCode(TruncatedBox) != 1 {
		t.Fatalf("code count wrong")
	}
	if l.Unrecoverable() {
		t.Fatal("all faults are recoverable")
	}
	l[0].Recoverable = false
	if !l.Unrecoverable() {
		t.Fatal("expected unrecoverable")
	}
}
