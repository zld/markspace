// Package packager turns an aligned plan into the manifest object model and
// the flat list of segment references used for template expansion.
package packager

import (
	"fmt"

	"dashgen/internal/align"
	"dashgen/internal/manifest"
	"dashgen/internal/mpd"
	"dashgen/internal/plan"
	"dashgen/internal/timebase"
)

// DefaultMinBufferTime is a conservative fixed value written into every MPD.
const DefaultMinBufferTimeMs int64 = 1500

// Package is the assembled, still-serializable presentation.
type Package struct {
	MPD      *manifest.MPD
	Refs     []mpd.SegmentRef
	Segments []mpd.ExpandedSegment
}

// Build validates the template by fully expanding it, then assembles one
// AdaptationSet with one Representation per plan rendition. mediaTemplate is
// the user supplied $...$ pattern; initTemplate may be empty.
func Build(p *plan.Plan, report *align.Report, mediaTemplate, initTemplate string) (*Package, error) {
	if len(report.Timelines) == 0 {
		return nil, fmt.Errorf("packager: report contains no timelines")
	}
	refs := buildRefs(report)
	expanded, err := mpd.ExpandAll(mediaTemplate, refs)
	if err != nil {
		return nil, err
	}
	if initTemplate != "" {
		for i := range p.Representations {
			if _, err := mpd.Expand(initTemplate, mpd.SegmentRef{
				RepID: p.Representations[i].ID, Number: 0, Time: 0,
			}); err != nil {
				return nil, fmt.Errorf("packager: initialization template: %w", err)
			}
		}
	}

	reps := make([]manifest.Representation, 0, len(report.Timelines))
	for i, tl := range report.Timelines {
		timeline, err := manifest.BuildTimeline(tl.Bounds)
		if err != nil {
			return nil, fmt.Errorf("packager: rep %q: %w", tl.RepID, err)
		}
		st := &manifest.SegmentTemplate{
			Timescale:       tl.Timescale,
			Media:           mediaTemplate,
			StartNumber:     firstNumber(tl.Bounds),
			SegmentTimeline: timeline,
		}
		if initTemplate != "" {
			st.Initialization = initTemplate
		}
		reps = append(reps, manifest.Representation{
			ID:              tl.RepID,
			Bandwidth:       p.Representations[i].BandwidthBPS,
			SegmentTemplate: st,
		})
	}

	duration, err := presentationDurationMs(report)
	if err != nil {
		return nil, err
	}
	minBuffer, err := timebase.TicksToMs(DefaultMinBufferTimeMs, 1000)
	if err != nil {
		return nil, err
	}
	doc := &manifest.MPD{
		Profiles:                  manifest.MainProfile,
		Type:                      "static",
		MediaPresentationDuration: manifest.FormatDuration(duration),
		MinBufferTime:             manifest.FormatDuration(minBuffer),
		Periods: []manifest.Period{{
			ID: "P0",
			AdaptationSets: []manifest.AdaptationSet{{
				ID:              0,
				ContentType:     "video",
				MimeType:        manifest.VideoMimeType,
				SegmentAligned:  true,
				Representations: reps,
			}},
		}},
	}
	return &Package{MPD: doc, Refs: refs, Segments: expanded}, nil
}

func buildRefs(report *align.Report) []mpd.SegmentRef {
	var refs []mpd.SegmentRef
	for _, tl := range report.Timelines {
		for _, b := range tl.Bounds {
			refs = append(refs, mpd.SegmentRef{
				RepID:  tl.RepID,
				Number: b.Number,
				Time:   b.StartTicks,
			})
		}
	}
	return refs
}

func firstNumber(bounds []align.SegmentBounds) int64 {
	if len(bounds) == 0 {
		return 1
	}
	return bounds[0].Number
}

// presentationDurationMs is the span from the earliest segment start to the
// latest segment end across all renditions, expressed in milliseconds.
func presentationDurationMs(report *align.Report) (timebase.Rational, error) {
	var minStart, maxEnd timebase.Rational
	have := false
	for _, tl := range report.Timelines {
		for _, b := range tl.Bounds {
			if !have {
				minStart, maxEnd, have = b.StartMs, b.EndMs, true
				continue
			}
			if b.StartMs.Cmp(minStart) < 0 {
				minStart = b.StartMs
			}
			if b.EndMs.Cmp(maxEnd) > 0 {
				maxEnd = b.EndMs
			}
		}
	}
	if !have {
		return timebase.Rational{}, fmt.Errorf("packager: no segments to measure")
	}
	return maxEnd.Sub(minStart)
}
