package merr

import (
	"errors"
	"fmt"
	"testing"
)

func TestStructuredFields(t *testing.T) {
	e := New(StageAudio, KindCorrupt, 3, 128, "bad block")
	msg := e.Error()
	for _, want := range []string{"stage=audio", "kind=corrupt", "track=3", "offset=128", "bad block"} {
		if !contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
}

func TestWrapUnwrap(t *testing.T) {
	cause := fmt.Errorf("eof")
	e := Wrap(StageProbe, KindTruncated, 0, 7, cause, "short box")
	if !errors.Is(e, cause) {
		t.Fatal("wrapped cause must be reachable")
	}
	got, ok := As(error(e))
	if !ok || got.Stage != StageProbe || got.Kind != KindTruncated || got.InputOffset != 7 {
		t.Fatalf("As failed: %+v", got)
	}
	if e.Error() == "" || !contains(e.Error(), "short box") {
		t.Fatalf("unexpected message %q", e.Error())
	}
}

func TestStageOf(t *testing.T) {
	if StageOf(nil) != "" {
		t.Fatal("nil has no stage")
	}
	if StageOf(errors.New("plain")) != "" {
		t.Fatal("plain error has no stage")
	}
	if StageOf(New(StageStream, KindLimit, 1, 0, "full")) != StageStream {
		t.Fatal("stage mismatch")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
