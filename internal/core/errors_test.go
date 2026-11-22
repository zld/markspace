package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorIncludesEveryLocation(t *testing.T) {
	e := New(CodeSampleRange, StageRecover, true, "sample runs past end").
		WithOffset(4096).WithTrack(7, 3).WithNode("resample")
	msg := e.Error()
	for _, want := range []string{"stage=recover", "code=sample_range", "offset=4096", "track=7", "sample=3", "node=resample", "sample runs past end"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
	if e.Unwrap() != nil {
		t.Fatal("New should not wrap")
	}
}

func TestWrapPreservesCause(t *testing.T) {
	cause := errors.New("disk gone")
	e := Wrap(CodeIO, StageManifest, false, "write manifest", cause)
	if !errors.Is(e, cause) {
		t.Fatal("wrapped cause not reachable")
	}
	if !strings.Contains(e.Error(), "disk gone") {
		t.Fatalf("cause text missing: %v", e)
	}
	var got *Error
	if !errors.As(fmt.Errorf("outer: %w", e), &got) || got.Code != CodeIO {
		t.Fatal("As did not find structured error through wrapping")
	}
}

func TestWithCopiesDoNotMutateOriginal(t *testing.T) {
	e := New(CodeBadTimestamp, StageScan, false, "zero timescale")
	a := e.WithOffset(100)
	b := a.WithTrack(2, 5)
	if e.Offset != 0 || e.Track != 0 {
		t.Fatal("original error mutated")
	}
	if b.Offset != 100 || b.Track != 2 || b.Sample != 5 {
		t.Fatalf("copies lost fields: %+v", b)
	}
}

func TestIsCodeThroughChain(t *testing.T) {
	e := New(CodeDependencyCycle, StageGraph, false, "cycle")
	wrapped := fmt.Errorf("run failed: %w", e)
	if !IsCode(wrapped, CodeDependencyCycle) {
		t.Fatal("IsCode missed wrapped cycle")
	}
	if IsCode(wrapped, CodeIO) {
		t.Fatal("IsCode matched the wrong code")
	}
	if IsCode(errors.New("plain"), CodeIO) {
		t.Fatal("plain error matched a code")
	}
}

func TestExitCodeContract(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{New(CodeDependencyCycle, StageGraph, false, "cycle"), 2},
		{New(CodeUnrecoverable, StageRecover, false, "gone"), 3},
		{New(CodeMissingTrack, StageGraph, false, "none"), 3},
		{New(CodeIO, StageScan, false, "io"), 1},
		{New(CodeParamConflict, StageGraph, false, "conflict"), 1},
		{New(CodeSampleRange, StageRecover, true, "range"), 1},
		{errors.New("plain"), 1},
		{fmt.Errorf("wrapped: %w", New(CodeDependencyCycle, StageGraph, false, "c")), 2},
	}
	for i, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("case %d: exit = %d, want %d", i, got, c.want)
		}
	}
}
