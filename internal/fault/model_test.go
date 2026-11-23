package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestFaultFormatting(t *testing.T) {
	f := Fatal(ClassIndex, "broken", "sample table is inconsistent").AtByte(128).OnTrack(3)
	got := f.Error()
	want := "index/broken track=3 at byte 128: sample table is inconsistent [unrecoverable]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRecoverableFormatting(t *testing.T) {
	f := New(ClassSample, "truncated", "short payload").AtTick(480)
	if !strings.Contains(f.Error(), "recoverable") {
		t.Fatalf("got %q", f.Error())
	}
}

func TestMatcherByClass(t *testing.T) {
	f := New(ClassAudio, "clip", "x")
	if !errors.Is(f, OfClass(ClassAudio, "")) {
		t.Fatal("class matcher failed")
	}
	if !errors.Is(f, OfClass(ClassAudio, "clip")) {
		t.Fatal("code matcher failed")
	}
	if errors.Is(f, OfClass(ClassAudio, "other")) {
		t.Fatal("wrong code matched")
	}
	if errors.Is(f, OfClass(ClassIndex, "")) {
		t.Fatal("wrong class matched")
	}
}

func TestWrappedFaultMatching(t *testing.T) {
	f := New(ClassSample, "truncated", "x").Because(errors.New("io"))
	wrapped := errors.Join(errors.New("outer"), f)
	if !errors.Is(wrapped, OfClass(ClassSample, "truncated")) {
		t.Fatal("wrapped fault did not match")
	}
}

func TestCollectorSortsDeterministically(t *testing.T) {
	var c Collector
	c.AddFault(New(ClassSample, "b", "two").AtByte(90))
	c.AddFault(New(ClassSample, "a", "one").AtByte(10))
	c.AddFault(Fatal(ClassContainer, "z", "fatal").AtByte(5))
	c.Add(errors.New("plain"))
	fs := c.Faults()
	if len(fs) != 4 {
		t.Fatalf("got %d faults", len(fs))
	}
	// container sorts before input before sample
	if fs[0].Class != ClassContainer || fs[1].Class != ClassInput {
		t.Fatalf("unexpected order: %v", fs)
	}
	if fs[2].Offset != 10 || fs[3].Offset != 90 {
		t.Fatalf("sample faults not offset-ordered: %v", fs)
	}
	first := strings.Join(c.Summary(), "\n")
	second := strings.Join(c.Summary(), "\n")
	if first != second {
		t.Fatal("summary not deterministic")
	}
}

func TestCollectorExitCodes(t *testing.T) {
	var clean Collector
	if got := clean.ExitCode(); got != 0 {
		t.Fatalf("clean exit %d", got)
	}
	var soft Collector
	soft.AddFault(New(ClassAudio, "clip", "x"))
	if got := soft.ExitCode(); got != 4 {
		t.Fatalf("recoverable exit %d", got)
	}
	var hard Collector
	hard.AddFault(Fatal(ClassDependency, "cycle", "x"))
	if got := hard.ExitCode(); got != 3 {
		t.Fatalf("fatal exit %d", got)
	}
	hard.AddFault(New(ClassAudio, "clip", "x"))
	if got := hard.ExitCode(); got != 3 {
		t.Fatalf("fatal must dominate, got %d", got)
	}
}

func TestCollectorNilErrorIgnored(t *testing.T) {
	var c Collector
	c.Add(nil)
	c.AddFault(Fault{})
	if c.Len() != 1 {
		t.Fatalf("got %d", c.Len())
	}
}
