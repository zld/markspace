package main

import (
	"fmt"
	"io"
	"os"

	"dashforge/internal/input"
	"dashforge/internal/model"
	"dashforge/internal/report"
	"dashforge/internal/segments"
	"dashforge/internal/timeline"
	"dashforge/internal/validate"
)

type config struct {
	input, output, mpd          string
	timescale, maxReps, maxSegs int
	tolerance                   float64
	asJSON, checkFiles          bool
}

func loadMetadata(cfg config, stderr io.Writer) (*model.Metadata, int) {
	meta, err := input.Load(cfg.input, cfg.maxReps, cfg.maxSegs)
	if err != nil {
		if _, ok := err.(input.Errors); ok {
			fmt.Fprintf(stderr, "error: invalid input metadata: %v\n", err)
			return nil, exitInvalid
		}
		fmt.Fprintf(stderr, "error: read metadata: %v\n", err)
		return nil, exitIOFailure
	}
	return meta, exitOK
}

func normalize(meta *model.Metadata, cfg config, stderr io.Writer) (*model.Manifest, int) {
	man, _, err := timeline.Normalize(meta, cfg.timescale)
	if err != nil {
		fmt.Fprintf(stderr, "error: timeline normalization: %v\n", err)
		return nil, exitInvalid
	}
	return man, exitOK
}

func expandAll(man *model.Manifest) ([]segments.RenditionResult, []segments.Issue, int) {
	expanded, issues := segments.Expand(man)
	return expanded, issues, exitOK
}

func issuesToFindings(issues []segments.Issue) validate.Findings {
	out := make(validate.Findings, 0, len(issues))
	for _, is := range issues {
		out = append(out, validate.Finding{Rendition: is.Rendition, Kind: is.Kind, Detail: is.Detail})
	}
	return out
}

func buildSummary(man *model.Manifest, expanded []segments.RenditionResult,
	tolerance float64, findings validate.Findings) report.Summary {
	sum := report.Summary{
		Representations: len(man.Renditions),
		Timescale:       man.Timescale,
		DurationMs:      float64(man.PeriodDuration()) * 1000 / float64(man.Timescale),
		ToleranceMs:     tolerance,
	}
	for _, rm := range man.Renditions {
		sum.Segments += len(rm.Segments)
	}
	for _, f := range findings {
		if f.ErrorMs > sum.MaxAlignmentErrorMs {
			sum.MaxAlignmentErrorMs = f.ErrorMs
		}
		sum.Findings = append(sum.Findings, report.FindingJSON{
			Rendition: f.Rendition, Kind: f.Kind, Detail: f.Detail,
			BoundaryMs: f.BoundaryMs, ErrorMs: f.ErrorMs,
		})
	}
	for _, r := range expanded {
		sum.Renditions = append(sum.Renditions, report.RenditionSummary{
			ID: r.RenditionID, Bandwidth: bandwidthOf(man, r.RenditionID),
			SegmentCount: r.Count, FirstSegment: r.First, LastSegment: r.Last,
		})
	}
	return sum
}

func bandwidthOf(man *model.Manifest, id string) int {
	if rm, err := man.FindRendition(id); err == nil {
		return rm.Bandwidth
	}
	return 0
}

func writeSummary(sum report.Summary, asJSON bool, w io.Writer) {
	if asJSON {
		data, err := sum.JSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: encode summary: %v\n", err)
			return
		}
		_, _ = w.Write(data)
		return
	}
	_, _ = io.WriteString(w, sum.Text())
}
