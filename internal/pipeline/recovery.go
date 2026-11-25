package pipeline

import (
	auditerrors "media-audit/internal/errors"
	"media-audit/internal/manifest"
	"media-audit/internal/recover"
	"media-audit/internal/task"
)

func buildRecovery(inputs []*parsedInput, item *manifest.Item, plan task.ResumePlan) *manifest.RecoverySummary {
	summary := &manifest.RecoverySummary{
		Stage:              "not-required",
		ResumeStage:        plan.ResumeStage,
		CompletedStage:     plan.CompletedStage,
		CorruptCheckpoints: plan.CorruptStages,
	}
	if len(item.Errors) > 0 {
		summary.Stage = "range-recovery"
	}

	var recompute []recover.Range
	for _, e := range item.Errors {
		in := findInput(inputs, e.Path)
		deps := dependenciesForError(in, e)
		rp := recover.PlanForError(e, deps, nil)
		summary.Plans = append(summary.Plans, rp)
		recompute = append(recompute, rp.Recompute...)
	}
	summary.Recomputed = recover.MergeRanges(recompute)

	var sampleRanges []recover.Range
	for _, in := range inputs {
		for _, tr := range in.Doc.Tracks {
			for _, s := range tr.Samples {
				sampleRanges = append(sampleRanges, recover.Range{Start: s.RecordStart, End: s.RecordEnd})
			}
		}
	}
	summary.Successful = recover.SubtractRanges(sampleRanges, summary.Recomputed)
	return summary
}

func findInput(inputs []*parsedInput, path string) *parsedInput {
	for _, in := range inputs {
		if in.Path == path {
			return in
		}
	}
	if len(inputs) == 1 {
		return inputs[0]
	}
	return nil
}

func dependenciesForError(in *parsedInput, e *auditerrors.Error) []recover.Range {
	if in == nil {
		return nil
	}
	tr, sample, found := findSampleAt(in.Doc, e.Offset)
	if !found {
		// Truncation EOF is not itself inside a sample; use byte-range start.
		if e.Bytes != nil {
			tr, sample, found = findSampleAt(in.Doc, e.Bytes.Start)
		}
	}
	if !found || tr == nil {
		return nil
	}
	var deps []recover.Range
	switch e.Code {
	case auditerrors.CodeTimestampWrap, auditerrors.CodeSequenceGap:
		for i := range tr.Samples {
			if tr.Samples[i].Index == sample.Index && i > 0 {
				prev := tr.Samples[i-1]
				deps = append(deps, recover.Range{Start: prev.RecordStart, End: prev.RecordEnd})
			}
		}
	default:
		if sample.DependsOn >= 0 {
			for _, cand := range tr.Samples {
				if cand.Index == uint32(sample.DependsOn) {
					deps = append(deps, recover.Range{Start: cand.RecordStart, End: cand.RecordEnd})
				}
			}
		}
	}
	return deps
}
