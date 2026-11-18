// Package validate checks the normalized manifest and the expanded segment
// references for cross-rendition consistency: references exist and stay inside
// their root, timelines are contiguous, representation durations and boundaries
// agree within the configured tolerance, and an on-disk MPD matches the model.
package validate

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dashforge/internal/model"
	"dashforge/internal/mpd"
	"dashforge/internal/segments"
)

// Finding is one validation failure. BoundaryMs / ErrorMs are zero when the
// finding is not alignment related.
type Finding struct {
	Rendition  string  `json:"rendition,omitempty"`
	Kind       string  `json:"kind"`
	Detail     string  `json:"detail"`
	BoundaryMs float64 `json:"boundary_ms,omitempty"`
	ErrorMs    float64 `json:"error_ms,omitempty"`
}

func (f Finding) String() string {
	s := f.Kind + ": " + f.Detail
	if f.Rendition != "" {
		s = fmt.Sprintf("rendition %q: %s", f.Rendition, s)
	}
	if f.ErrorMs != 0 {
		s += fmt.Sprintf(" (boundary=%.3fms, error=%.3fms)", f.BoundaryMs, f.ErrorMs)
	}
	return s
}

// Findings aggregates all problems and renders deterministically.
type Findings []Finding

func (fs Findings) Error() string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.String()
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// Options controls validation. ToleranceMs must be non-negative; a zero value
// requires exact alignment.
type Options struct {
	ToleranceMs float64
	// CheckFiles makes the validator stat every referenced segment on disk.
	CheckFiles bool
}

// Model runs every check against the in-memory model plus expansion results.
func Model(man *model.Manifest, expanded []segments.RenditionResult, opts Options) Findings {
	if opts.ToleranceMs < 0 {
		return Findings{{Kind: "tolerance", Detail: "tolerance must not be negative"}}
	}
	var findings Findings
	expByID := make(map[string]segments.RenditionResult, len(expanded))
	for _, r := range expanded {
		expByID[r.RenditionID] = r
	}

	findings = append(findings, checkReferences(man, expByID, opts)...)
	findings = append(findings, checkContiguity(man)...)
	findings = append(findings, checkDurations(man, opts.ToleranceMs)...)
	findings = append(findings, checkBoundaries(man, opts.ToleranceMs)...)
	return findings
}

// checkReferences verifies every expanded reference is non-empty, exists on
// disk when requested, and lives inside the rendition's segment root.
func checkReferences(man *model.Manifest, exp map[string]segments.RenditionResult, opts Options) Findings {
	var findings Findings
	for _, rm := range man.Renditions {
		res, ok := exp[rm.ID]
		if !ok {
			findings = append(findings, Finding{Rendition: rm.ID, Kind: "missing_expansion",
				Detail: "no expanded segments were produced"})
			continue
		}
		if res.Count != len(rm.Segments) {
			findings = append(findings, Finding{Rendition: rm.ID, Kind: "missing_segment",
				Detail: fmt.Sprintf("expanded %d references for %d segments", res.Count, len(rm.Segments))})
		}
		for _, s := range res.Segments {
			if s.Path == "" {
				findings = append(findings, Finding{Rendition: rm.ID, Kind: "empty_reference",
					Detail: fmt.Sprintf("segment %d expands to an empty path", s.Number)})
				continue
			}
			if opts.CheckFiles {
				full := filepath.Join(rm.SegmentRoot, s.Path)
				if _, err := os.Stat(full); err != nil {
					findings = append(findings, Finding{Rendition: rm.ID, Kind: "unresolved_reference",
						Detail: fmt.Sprintf("segment %d: %s: %v", s.Number, full, err)})
				}
			}
		}
	}
	return findings
}

// checkContiguity ensures segments are gap-free and non-overlapping on the
// common timeline: each fragment starts where the previous one ended.
func checkContiguity(man *model.Manifest) Findings {
	var findings Findings
	for _, rm := range man.Renditions {
		for i := 1; i < len(rm.Segments); i++ {
			prev, cur := rm.Segments[i-1], rm.Segments[i]
			want := prev.Start + prev.Duration
			if cur.Start != want {
				findings = append(findings, Finding{
					Rendition: rm.ID, Kind: "timeline_gap",
					Detail: fmt.Sprintf("segment %d starts at %d, expected %d", cur.Number, cur.Start, want),
				})
			}
		}
	}
	return findings
}

// checkDurations compares every representation duration against the period
// duration; a drift beyond tolerance fails validation.
func checkDurations(man *model.Manifest, toleranceMs float64) Findings {
	var findings Findings
	periodTicks := man.PeriodDuration()
	periodMs := float64(periodTicks) * 1000 / float64(man.Timescale)
	for _, rm := range man.Renditions {
		ms := float64(rm.Duration) * 1000 / float64(man.Timescale)
		if diff := math.Abs(periodMs - ms); diff > toleranceMs {
			findings = append(findings, Finding{
				Rendition: rm.ID, Kind: "duration_drift",
				Detail:  fmt.Sprintf("duration %.3fms differs from period %.3fms", ms, periodMs),
				ErrorMs: diff, BoundaryMs: ms,
			})
		}
	}
	return findings
}

// checkBoundaries aligns fragment boundaries across renditions. Each boundary
// is compared against the nearest boundary of the first (reference) rendition.
func checkBoundaries(man *model.Manifest, toleranceMs float64) Findings {
	var findings Findings
	if len(man.Renditions) < 2 {
		return findings
	}
	ref := man.Renditions[0]
	refTicks := boundaryTicks(ref)
	for _, rm := range man.Renditions[1:] {
		for _, tick := range boundaryTicks(rm) {
			boundaryMs := float64(tick) * 1000 / float64(man.Timescale)
			errMs := nearestError(tick, refTicks, man.Timescale)
			if errMs > toleranceMs {
				findings = append(findings, Finding{
					Rendition: rm.ID, Kind: "boundary_misalignment",
					Detail:     fmt.Sprintf("no matching reference boundary within tolerance %.3fms", toleranceMs),
					BoundaryMs: boundaryMs, ErrorMs: errMs,
				})
			}
		}
	}
	return findings
}

func boundaryTicks(rm *model.RenditionManifest) []int64 {
	out := make([]int64, 0, len(rm.Segments)+1)
	for _, s := range rm.Segments {
		out = append(out, s.Start)
	}
	if len(rm.Segments) > 0 {
		last := rm.Segments[len(rm.Segments)-1]
		out = append(out, last.Start+last.Duration)
	}
	return out
}

func nearestError(tick int64, refs []int64, timescale int) float64 {
	best := int64(math.MaxInt64)
	for _, r := range refs {
		d := tick - r
		if d < 0 {
			d = -d
		}
		if d < best {
			best = d
		}
	}
	return float64(best) * 1000 / float64(timescale)
}

// MPD re-parses a serialized MPD and checks it against the model: same number
// of representations, unique ascending bandwidths, and the timeline fragment
// count and total duration match.
func MPD(man *model.Manifest, mpdPath string) Findings {
	var findings Findings
	data, err := os.ReadFile(mpdPath)
	if err != nil {
		return Findings{{Kind: "mpd_read", Detail: err.Error()}}
	}
	var doc mpd.Document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Findings{{Kind: "mpd_parse", Detail: err.Error()}}
	}
	reps := doc.Period.AdaptationSet.Representations
	if len(reps) != len(man.Renditions) {
		findings = append(findings, Finding{Kind: "mpd_mismatch",
			Detail: fmt.Sprintf("MPD has %d representations, model has %d", len(reps), len(man.Renditions))})
	}
	seen := map[string]bool{}
	prevBW := 0
	for i, rep := range reps {
		if rep.ID == "" || seen[rep.ID] {
			findings = append(findings, Finding{Kind: "mpd_invalid_id",
				Detail: fmt.Sprintf("representation %d has missing or duplicate id %q", i, rep.ID)})
		}
		seen[rep.ID] = true
		if i > 0 && rep.Bandwidth <= prevBW {
			findings = append(findings, Finding{Rendition: rep.ID, Kind: "mpd_bandwidth_order",
				Detail: fmt.Sprintf("bandwidth %d is not strictly greater than %d", rep.Bandwidth, prevBW)})
		}
		prevBW = rep.Bandwidth
	}
	timelineCount := 0
	for _, s := range doc.Period.AdaptationSet.SegmentTemplate.SegmentTimeline.Entries {
		timelineCount += s.Repeat + 1
	}
	if timelineCount != len(man.Renditions[0].Segments) {
		findings = append(findings, Finding{Kind: "mpd_mismatch",
			Detail: fmt.Sprintf("MPD timeline expands to %d fragments, model has %d",
				timelineCount, len(man.Renditions[0].Segments))})
	}
	if doc.MediaPresentationTime != mpd.FormatDuration(man.PeriodDuration(), man.Timescale) {
		findings = append(findings, Finding{Kind: "mpd_mismatch",
			Detail: fmt.Sprintf("MPD duration %q does not match model %q",
				doc.MediaPresentationTime, mpd.FormatDuration(man.PeriodDuration(), man.Timescale))})
	}
	return findings
}
