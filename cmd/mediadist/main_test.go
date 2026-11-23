package main

import (
	"errors"
	"testing"

	"mediadist/pipeline"
	"mediadist/timeline"
)

func TestClassifyExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, exitOK},
		{"bad argument", pipeline.NewRunErrorForTest(timeline.CodeBadArgument, errors.New("x")), exitUsage},
		{"truncated", pipeline.NewRunErrorForTest(timeline.CodeTruncatedIndex, errors.New("x")), exitUnrecover},
		{"timestamp back", pipeline.NewRunErrorForTest(timeline.CodeTimestampBack, errors.New("x")), exitUnrecover},
		{"bad layout", pipeline.NewRunErrorForTest(timeline.CodeBadLayout, errors.New("x")), exitUnrecover},
		{"color conflict", pipeline.NewRunErrorForTest(timeline.CodeColorConflict, errors.New("x")), exitUnrecover},
		{"missing keyframe", pipeline.NewRunErrorForTest(timeline.CodeMissingKeyframe, errors.New("x")), exitUnrecover},
		{"internal", pipeline.NewRunErrorForTest("INTERNAL", errors.New("x")), exitInternal},
		{"plain loc error", timeline.NewLocError(timeline.CodeColorPending, timeline.StageNormalize, 1, -1, "p", nil), exitUnrecover},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyExit(tc.err); got != tc.want {
				t.Fatalf("classifyExit(%v) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestParseBitrates(t *testing.T) {
	got, err := parseBitrates("800k,1600k,3000k")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{800_000, 1_600_000, 3_000_000}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d want %d", i, got[i], want[i])
		}
	}
	if _, err := parseBitrates("3m"); err != nil {
		t.Fatalf("mega suffix: %v", err)
	}
	if _, err := parseBitrates(""); err == nil {
		t.Fatal("empty bitrates must error")
	}
}

func TestErrorResolution(t *testing.T) {
	le := timeline.NewLocError(timeline.CodeBadLayout, timeline.StageTimeline, 7, 3, "op", errors.New("boom"))
	if code := errorCode(le); code != timeline.CodeBadLayout {
		t.Fatalf("errorCode=%q want LAYOUT_UNSUPPORTED", code)
	}
	re := pipeline.NewRunErrorForTest("INTERNAL", errors.New("wrapped"))
	if code := errorCode(re); code != "INTERNAL" {
		t.Fatalf("errorCode=%q want INTERNAL", code)
	}
}
