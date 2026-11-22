package merrors

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatIncludesPosition(t *testing.T) {
	e := At(StageAdapter, Truncated, 4096, "sample %d truncated", 7)
	msg := e.Error()
	for _, want := range []string{"adapter.mp4v1", "truncated", "offset=4096", "sample 7 truncated"} {
		if !contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestErrorFormatIncludesPacketIdentity(t *testing.T) {
	e := Packet(StageRTPReorder, Late, 0xAABBCCDD, 65535, "behind window")
	msg := e.Error()
	for _, want := range []string{"rtp.reorder", "late", "ssrc=2864434397", "seq=65535"} {
		if !contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestErrorFormatOmitsUnsetPosition(t *testing.T) {
	e := New(StageJitter, Gap, "a hole")
	msg := e.Error()
	if contains(msg, "offset=") || contains(msg, "ssrc=") || contains(msg, "seq=") {
		t.Fatalf("unset fields leaked: %q", msg)
	}
}

func TestUnwrapAndCategoryLookup(t *testing.T) {
	root := errors.New("underlying failure")
	e := Wrap(StageFEC, Checksum, 128, root, "parity mismatch")
	if !errors.Is(e, root) {
		t.Fatal("Unwrap does not reach the cause")
	}
	if cat, ok := CategoryOf(e); !ok || cat != Checksum {
		t.Fatalf("category = %v %v", cat, ok)
	}
	if stage, ok := StageOf(e); !ok || stage != StageFEC {
		t.Fatalf("stage = %v %v", stage, ok)
	}
	if !IsCategory(e, Checksum) || IsCategory(e, Truncated) {
		t.Fatal("IsCategory misclassified")
	}
	if !contains(e.Error(), "parity mismatch") || !contains(e.Error(), "underlying failure") {
		t.Fatalf("wrapped message incomplete: %q", e.Error())
	}
}

func TestCategoryLookupThroughWrapping(t *testing.T) {
	root := Wrap(StageTimeline, OutOfRange, 1000, nil, "jump")
	wrapped := fmt.Errorf("pipeline: %w", root)
	cat, ok := CategoryOf(wrapped)
	if !ok || cat != OutOfRange {
		t.Fatalf("category through fmt wrap = %v %v", cat, ok)
	}
}

func TestNonMediaError(t *testing.T) {
	if _, ok := CategoryOf(errors.New("plain")); ok {
		t.Fatal("plain error reported a category")
	}
	if _, ok := StageOf(errors.New("plain")); ok {
		t.Fatal("plain error reported a stage")
	}
	if IsCategory(nil, Gap) {
		t.Fatal("nil reported as a category")
	}
}

func TestZeroValuePacketDoesNotClaimIdentity(t *testing.T) {
	// A packet error with zero SSRC and sequence must not print identity
	// fields, so a real zero-valued sequence is not mistaken for "unset".
	e := Packet(StageRTPParse, Malformed, 0, 0, "bad header")
	if contains(e.Error(), "ssrc=") {
		t.Fatalf("zero identity leaked: %q", e.Error())
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
