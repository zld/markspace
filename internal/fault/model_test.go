package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewAndErrorFormat(t *testing.T) {
	e := New(KindTruncated, 42, "cluster ends at %d", 40)
	if e.Offset != 42 || e.Kind != KindTruncated || e.Recoverable {
		t.Fatalf("unexpected error state: %+v", e)
	}
	if got := e.Error(); got != "truncated: offset=42: cluster ends at 40" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestTrackErrorFormat(t *testing.T) {
	e := NewTrack(KindUnsupportedCodec, 3, 100, "codec %s", "foo")
	if got := e.Error(); got != "unsupported_codec: track=3 offset=100: codec foo" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestKindMatching(t *testing.T) {
	base := New(KindIllegalVINT, 7, "bad marker")
	wrapped := fmtWrap(base)
	if !Is(wrapped, KindIllegalVINT) {
		t.Fatal("errors.Is kind matching failed through wrap")
	}
	if Is(wrapped, KindTruncated) {
		t.Fatal("different kind must not match")
	}
	if Of(wrapped) != base {
		t.Fatal("Of must extract the embedded structured error")
	}
	if Of(nil) != nil {
		t.Fatal("Of(nil) must be nil")
	}
}

func TestRecoverableFlag(t *testing.T) {
	e := New(KindCheckpointCorrupt, 0, "bad checksum").AsRecoverable()
	if !IsRecoverable(e) {
		t.Fatal("recoverable flag not propagated")
	}
	if IsRecoverable(errors.New("plain")) {
		t.Fatal("plain errors are not recoverable")
	}
}

func fmtWrap(e error) error { return fmt.Errorf("outer: %w", e) }
