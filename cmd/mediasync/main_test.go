package main

import (
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/pipeline"
)

func TestRunRejectsInvalidArguments(t *testing.T) {
	cases := [][]string{
		nil,
		{"--self-test", "--loss", "150"},
		{"--self-test", "--jitter-ms", "-1"},
		{"--self-test", "--budget-ms", "-5"},
		{"--self-test", "--max-bytes", "-1"},
		{"--self-test", "--unknown-flag"},
	}
	for _, args := range cases {
		if code := run(args); code != 2 {
			t.Fatalf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunSelfTestPasses(t *testing.T) {
	args := []string{"--self-test", "--seed", "17", "--loss", "2",
		"--jitter-ms", "80", "--budget-ms", "500", "--max-bytes", "4096"}
	if code := run(args); code != 0 {
		t.Fatalf("acceptance run exit = %d, want 0", code)
	}
}

func TestCoreLanesCompleteDetectsIncompleteness(t *testing.T) {
	// A complete report: both core lanes kept and present in aligned segments.
	good := &pipeline.Report{
		BudgetDecisions: []pipeline.BudgetDecision{
			{Representation: "dialogue-core", Priority: model.PriorityCore, Action: "kept"},
			{Representation: "subtitle-core", Priority: model.PriorityCore, Action: "kept"},
		},
		AlignedSegments: []model.SegmentDescriptor{
			{Representation: "dialogue-core"},
			{Representation: "subtitle-core"},
		},
	}
	if !coreLanesComplete(good) {
		t.Fatal("complete core lanes must pass the assertion")
	}

	// A suspended core lane must fail the assertion (drives exit code 1).
	suspended := &pipeline.Report{
		BudgetDecisions: []pipeline.BudgetDecision{
			{Representation: "dialogue-core", Priority: model.PriorityCore, Action: "kept"},
			{Representation: "subtitle-core", Priority: model.PriorityCore, Action: "suspended-budget"},
		},
		AlignedSegments: []model.SegmentDescriptor{{Representation: "dialogue-core"}},
	}
	if coreLanesComplete(suspended) {
		t.Fatal("a suspended core lane must fail the assertion")
	}

	// A kept core lane with no aligned output must also fail.
	missing := &pipeline.Report{
		BudgetDecisions: []pipeline.BudgetDecision{
			{Representation: "dialogue-core", Priority: model.PriorityCore, Action: "kept"},
			{Representation: "subtitle-core", Priority: model.PriorityCore, Action: "kept"},
		},
		AlignedSegments: []model.SegmentDescriptor{{Representation: "dialogue-core"}},
	}
	if coreLanesComplete(missing) {
		t.Fatal("a core lane with no segments must fail the assertion")
	}
}

func TestOnlyCorruptLaneFailed(t *testing.T) {
	// Only the deliberately corrupt lane, classified as truncation, is allowed.
	clean := &pipeline.Report{Errors: []*model.MediaError{
		model.NewError(model.KindTruncatedMP4, "verify", 0,
			"rep sfx-corrupt fixture is corrupt"),
	}}
	if !onlyCorruptLaneFailed(clean) {
		t.Fatal("the single corrupt-lane truncation must be acceptable")
	}

	// A non-truncation verify error on another lane must be rejected.
	dirty := &pipeline.Report{Errors: []*model.MediaError{
		model.NewError(model.KindTruncatedMP4, "verify", 0, "rep sfx-corrupt x"),
		model.NewError(model.KindMalformedMP4, "verify", 0, "rep video-hi mismatch"),
	}}
	if onlyCorruptLaneFailed(dirty) {
		t.Fatal("a healthy-lane verify failure must not be masked by the corrupt lane")
	}
}
