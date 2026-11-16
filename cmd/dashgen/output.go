package main

import (
	"errors"
	"fmt"

	"dashgen/internal/align"
	"dashgen/internal/mpd"
	"dashgen/internal/packager"
	"dashgen/internal/timebase"
	"dashgen/internal/validate"
)

// printTimelineErrors expands the joined aligner error into one stable line
// per structural defect. A nil error prints nothing.
func printTimelineErrors(err error) {
	if err == nil {
		return
	}
	for _, e := range split(err) {
		var te *align.TimelineError
		var me *align.MissingError
		switch {
		case errors.As(e, &te):
			fmt.Printf("timeline_error=%s rep=%s segment=%d detail=%q\n",
				te.Kind, te.RepID, te.Number, te.Msg)
		case errors.As(e, &me):
			fmt.Printf("timeline_error=%s segment=%d present=%v missing=%v\n",
				align.KindMissing, me.Number, me.Present, me.Missing)
		default:
			fmt.Printf("timeline_error=%v\n", e)
		}
	}
}

func split(err error) []error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		return joined.Unwrap()
	}
	return []error{err}
}

func printSummary(segments []mpd.ExpandedSegment, report *align.Report, pkg *packager.Package) {
	fmt.Printf("representation_count=%d\n", len(report.Timelines))
	fmt.Printf("expanded_segment_count=%d\n", len(segments))
	fmt.Printf("max_start_offset_ms=%s\n", timebase.FormatMillis(report.MaxStartOffset))
	fmt.Printf("max_duration_delta_ms=%s\n", timebase.FormatMillis(report.MaxDurationDelta))
	if len(pkg.Segments) > 0 {
		fmt.Printf("template_example=%s\n", pkg.Segments[0].Path)
	}
}

func printSummaryFromVerification(v *validate.Verification) {
	fmt.Printf("representation_count=%d\n", v.RepresentationCount)
	fmt.Printf("expanded_segment_count=%d\n", v.ExpandedSegmentCount)
	fmt.Printf("max_start_offset_ms=%s\n", timebase.FormatMillis(v.MaxStartOffset))
	fmt.Printf("max_duration_delta_ms=%s\n", timebase.FormatMillis(v.MaxDurationDelta))
	if v.TemplateExample != "" {
		fmt.Printf("template_example=%s\n", v.TemplateExample)
	}
}

// printViolations lists every over-limit representation and segment
// individually, as required for tolerance failures.
func printViolations(res *validate.Result) {
	for _, v := range res.Violations {
		fmt.Printf("violation=%s rep=%s ref=%s segment=%d delta_ms=%s limit_ms=%s\n",
			v.Kind, v.RepID, v.RefID, v.Number,
			timebase.FormatMillis(v.DeltaMs), timebase.FormatMillis(v.LimitMs))
	}
}
