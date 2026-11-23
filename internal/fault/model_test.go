package fault

import (
	"errors"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	e := New(CodeNegativeDuration, 12, 340, "cue end before start", "fix cue timing")
	want := "E_NEGATIVE_DURATION: line=12 offset=340 cue end before start (hint: fix cue timing)"
	if e.Error() != want {
		t.Fatalf("got %q want %q", e.Error(), want)
	}
}

func TestIsCode(t *testing.T) {
	e := New(CodeTruncatedInput, 0, -1, "eof mid cue", "re-deliver full file")
	if !IsCode(e, CodeTruncatedInput) {
		t.Fatal("IsCode should match")
	}
	if IsCode(e, CodeBadTimecode) {
		t.Fatal("IsCode should not match a different code")
	}
	if IsCode(errors.New("plain"), CodeTruncatedInput) {
		t.Fatal("IsCode should reject non-fault errors")
	}
}
