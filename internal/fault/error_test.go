package fault

import (
	"errors"
	"testing"

	"liveregress/internal/clock"
)

func TestErrorCarriesAllFields(t *testing.T) {
	err := New(KindTruncate, "media/a1/seg2.m4s", 4096, "partial sample").
		WithTrack(3, "center").
		AtTime(clock.Must(48000, 48000)).
		InLayer("a1").
		Recover(true)
	msg := err.Error()
	for _, want := range []string{
		"truncate", "path=media/a1/seg2.m4s", "offset=4096", "track=3(center)",
		"time=1.000000s", "layer=a1", "recoverable=true", "partial sample",
	} {
		if !contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestErrorsIsByKind(t *testing.T) {
	if !errors.Is(New(KindTruncate, "x", 1, ""), ErrTruncated) {
		t.Fatal("truncate should match ErrTruncated")
	}
	if errors.Is(New(KindCorrupt, "x", 1, ""), ErrTruncated) {
		t.Fatal("corrupt must not match ErrTruncated")
	}
}

func TestExitCodes(t *testing.T) {
	if Code(nil) != 0 {
		t.Fatal("nil should be 0")
	}
	if Code(New(KindTruncate, "x", 1, "").Recover(true)) != 2 {
		t.Fatal("recoverable truncate should be 2")
	}
	if Code(New(KindTruncate, "x", 1, "").Recover(false)) != 1 {
		t.Fatal("non-recoverable truncate should be 1")
	}
	if Code(New(KindMapping, "x", 1, "")) != 1 {
		t.Fatal("mapping should be 1")
	}
	if Code(errors.New("plain")) != 1 {
		t.Fatal("unknown should be 1")
	}
}

func TestSummaryDeterministic(t *testing.T) {
	var s Summary
	s.Add(New(KindCorrupt, "b.m4s", 8, "").Recover(true))
	s.Add(New(KindTruncate, "a.m4s", 99, "").Recover(true))
	s.Add(New(KindTruncate, "a.m4s", 4, "").Recover(true))
	if s.ExitCode() != 2 {
		t.Fatalf("expected 2, got %d", s.ExitCode())
	}
	sorted := s.Sorted()
	if sorted[0].Path != "a.m4s" || sorted[0].Offset != 4 {
		t.Fatalf("not sorted: %+v", sorted[0])
	}
	s.Add(New(KindMapping, "c", 0, ""))
	if s.ExitCode() != 1 {
		t.Fatal("non-recoverable fault should force 1")
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
