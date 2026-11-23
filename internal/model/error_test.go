package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeRecoverability(t *testing.T) {
	unrecoverable := []Code{
		CodeUnrecoverableLoss, CodeTruncatedFragment, CodeOutOfWindow,
		CodeInvariant, CodeZeroTimescale, CodeChannelDrop, CodeSyncDrift,
	}
	for _, c := range unrecoverable {
		if c.Recoverable() {
			t.Errorf("%s must be unrecoverable", c)
		}
	}
	recoverable := []Code{CodeMissingReference, CodeDuplicateSeq, CodeTimestampJump, CodeColorConflict}
	for _, c := range recoverable {
		if !c.Recoverable() {
			t.Errorf("%s expected recoverable", c)
		}
	}
}

func TestMediaErrorCoordinates(t *testing.T) {
	err := NewError(CodeUnrecoverableLoss, StageRecovery, "mat-2", "video-0",
		"two packets lost in one FEC group")
	if err.Recoverable {
		t.Fatal("double loss must be unrecoverable")
	}
	s := err.Error()
	for _, want := range []string{"unrecoverable_loss", "recovery", "mat-2", "video-0", "unrecoverable"} {
		if !contains(s, want) {
			t.Fatalf("error %q missing coordinate %q", s, want)
		}
	}
}

func TestWrapAndHasCode(t *testing.T) {
	base := fmt.Errorf("short read")
	err := Wrap(base, CodeTruncatedFragment, StageParse, "mat-3", "audio-1", "box truncated")
	if !errors.Is(err, base) {
		t.Fatal("Unwrap chain broken")
	}
	if !HasCode(err, CodeTruncatedFragment) {
		t.Fatal("HasCode failed on wrapped error")
	}
	if IsRecoverable(err) {
		t.Fatal("truncated fragment must not be recoverable")
	}
	if !IsRecoverable(nil) {
		t.Fatal("nil error must report recoverable")
	}
}

func TestGapCount(t *testing.T) {
	g := Gap{FirstSeq: 10, LastSeq: 12}
	if g.Count() != 3 {
		t.Fatalf("gap count = %d, want 3", g.Count())
	}
}

func TestEventEnd(t *testing.T) {
	ev := Event{Start: tb().At(100), Duration: tb().At(25)}
	if ev.End().Ticks() != 125 {
		t.Fatalf("end = %d, want 125", ev.End().Ticks())
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
