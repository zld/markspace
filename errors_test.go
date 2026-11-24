package mediacore

import (
	"errors"
	"testing"
)

func TestErrorClassification(t *testing.T) {
	err := fail(KindIndex, "scan", "boom %d", 7)
	if !IsKind(err, KindIndex) {
		t.Fatalf("IsKind index failed for %v", err)
	}
	if KindOf(err) != KindIndex {
		t.Fatalf("KindOf = %q, want index", KindOf(err))
	}
	if !errors.Is(err, ErrIndexStale) {
		// Two *Error values share Kind via the custom Is method only when the
		// target carries the same kind; ErrIndexStale is KindIndex.
		t.Fatalf("errors.Is should match kind: %v", err)
	}
	wrapped := wrapErr(KindIO, "cache", errors.New("disk on fire"))
	if !IsKind(wrapped, KindIO) {
		t.Fatalf("wrapped kind not visible: %v", wrapped)
	}
	if !errors.Is(wrapped, wrapped) {
		t.Fatal("wrapped error must satisfy errors.Is identity")
	}
	if KindOf(errors.New("plain")) != "" {
		t.Fatal("plain error must have empty kind")
	}
}

func TestGapCountAndLifecycle(t *testing.T) {
	g := Gap{FirstSeq: 4, LastSeq: 9, State: GapMissing}
	if g.Count() != 6 {
		t.Fatalf("count = %d, want 6", g.Count())
	}
	if (Gap{FirstSeq: 9, LastSeq: 4}).Count() != 0 {
		t.Fatal("reversed range must count zero")
	}

	var log GapLog
	log.Add(Gap{TrackID: 1, FirstSeq: 1, LastSeq: 2, State: GapPending})
	log.Add(Gap{TrackID: 1, FirstSeq: 3, LastSeq: 3, State: GapPending})
	if log.MissingCount() != 0 {
		t.Fatal("pending gaps must not count as missing")
	}
	if log.Resolved() {
		t.Fatal("log with pending gaps must not be resolved")
	}
	term := log.Terminal()
	if term.MissingCount() != 3 {
		t.Fatalf("terminal missing = %d, want 3", term.MissingCount())
	}
	// Terminal must be a copy: mutating it cannot change the original.
	if log.MissingCount() != 0 {
		t.Fatal("Terminal mutated the original gap log")
	}
	if !term.Resolved() {
		t.Fatal("terminal log must be resolved")
	}
	if got := term.Gaps[0].String(); got == "" {
		t.Fatal("Gap.String must not be empty")
	}
}
