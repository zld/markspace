package fault

import (
	"errors"
	"testing"
)

func TestIssueFormatting(t *testing.T) {
	loc := Location{ByteOffset: 96, Tick: 48000, HasTick: true, Millis: 1000}
	i := New(KindTruncated, Damaged, loc, "wav data shorter than declared")
	s := i.Error()
	for _, want := range []string{"damaged", "byte 96", "tick 48000", "1000ms"} {
		if !contains(s, want) {
			t.Fatalf("issue %q missing %q", s, want)
		}
	}
}

func TestListKindsAndSeverity(t *testing.T) {
	var l List
	l.Add(New(KindDuplicatePacket, Warn, ByteAt(0), "dup"))
	l.Add(New(KindPacketLoss, Damaged, ByteAt(4), "lost"))
	if !l.HasKind(KindPacketLoss) {
		t.Fatal("missing loss kind")
	}
	if l.AtLeast(Fatal) {
		t.Fatal("no fatal issue expected")
	}
	if !l.AtLeast(Damaged) {
		t.Fatal("expected damaged")
	}
	first, ok := l.FirstAtLeast(Damaged)
	if !ok || first.Kind != KindPacketLoss {
		t.Fatalf("first damaged = %+v, %v", first, ok)
	}
	if err := error(&l); err == nil {
		t.Fatal("list should be an error")
	}
}

func TestErrorsAsThroughList(t *testing.T) {
	cause := errors.New("underlying")
	var l List
	l.Add(Wrap(KindBadChunk, Fatal, ByteAt(12), cause, "chunk header"))
	var target Issue
	if !errors.As(&l, &target) {
		t.Fatal("errors.As should locate the first issue inside a list")
	}
	if !errors.Is(issueErr(&l), cause) {
		t.Fatal("cause should be reachable")
	}
	if !IsKind(issueErr(&l), KindBadChunk) {
		t.Fatal("IsKind should find wrapped kind")
	}
}

func issueErr(l *List) error { return l }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
