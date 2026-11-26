package clock

import (
	"errors"
	"testing"
)

func TestErrorCarriesSourcePosition(t *testing.T) {
	e := New(CodeTSCCJump, "ts:pid=0x100", 4700, 12_000, 24_000,
		"CC jumped from 3 to 7")
	if e.Code != CodeTSCCJump || e.Offset != 4700 {
		t.Fatalf("code/offset lost: %+v", e)
	}
	if e.FromNanos != 12_000 || e.ToNanos != 24_000 {
		t.Fatalf("time range lost: %+v", e)
	}
	s := e.Error()
	for _, want := range []string{"ts:pid=0x100", "offset=4700", string(CodeTSCCJump)} {
		if !contains(s, want) {
			t.Fatalf("error %q missing %q", s, want)
		}
	}
}

func TestErrorIsByCode(t *testing.T) {
	e1 := New(CodeTSSync, "ts", 0, 0, 0, "bad byte")
	e2 := New(CodeTSSync, "ts", 9, 0, 0, "other")
	if !errors.Is(e1, e2) {
		t.Fatal("same-code errors should match")
	}
	e3 := New(CodeTSTruncated, "ts", 1, 0, 0, "x")
	if errors.Is(e1, e3) {
		t.Fatal("different codes must not match")
	}
	if AsError(nil) != nil {
		t.Fatal("AsError(nil) must be nil")
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
