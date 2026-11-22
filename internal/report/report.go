// Package report renders the human-readable diagnostic report from the
// same model.Manifest that feeds the MPD writer. Segment templates are
// expanded entry by entry from each representation's own numbers and
// ticks rather than printed as opaque template strings.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"dashcheck/internal/model"
)

// Options controls what the report includes.
type Options struct {
	MPDWritten bool
	MPDPath    string
	Loose      bool
}

// Write emits a deterministic, key=value style report to w.
func Write(w io.Writer, m *model.Manifest, opts Options) error {
	var b strings.Builder
	expanded := 0
	maxDelta := model.ZeroRat
	for _, as := range m.Period.AdaptationSets {
		for _, rep := range as.Representations {
			expanded += len(rep.Template.Timeline)
			if c, _ := rep.DeltaToLongestMs.Compare(maxDelta); c > 0 {
				maxDelta = rep.DeltaToLongestMs
			}
		}
	}

	fmt.Fprintf(&b, "content_id=%s\n", m.ContentID)
	fmt.Fprintf(&b, "representations=%d\n", countReps(m))
	fmt.Fprintf(&b, "expanded_segments=%d\n", expanded)
	fmt.Fprintf(&b, "common_switch_points=%d\n", len(m.SwitchPoints))
	fmt.Fprintf(&b, "max_boundary_skew_ms=%s\n", m.MaxBoundarySkewMs.DecimalFixed(3))
	fmt.Fprintf(&b, "duration_delta_ms=%s\n", maxDelta.DecimalFixed(3))
	fmt.Fprintf(&b, "tolerance_s=%s\n", m.Tolerance.String())
	fmt.Fprintf(&b, "mpd_written=%t\n", opts.MPDWritten)
	if opts.MPDWritten {
		fmt.Fprintf(&b, "mpd_path=%s\n", opts.MPDPath)
	}
	writeRepresentations(&b, m)
	writeSwitchPoints(&b, m)
	writeRejections(&b, m, opts.Loose)
	_, err := io.WriteString(w, b.String())
	return err
}

func countReps(m *model.Manifest) int {
	n := 0
	for _, as := range m.Period.AdaptationSets {
		n += len(as.Representations)
	}
	return n
}

func writeRepresentations(b *strings.Builder, m *model.Manifest) {
	reps := orderedReps(m)
	for _, rep := range reps {
		fmt.Fprintf(b, "representation id=%s bandwidth=%d size=%dx%d timescale=%d "+
			"start_s=%s end_s=%s duration_s=%s delta_to_longest_ms=%s\n",
			rep.ID, rep.Bandwidth, rep.Width, rep.Height, rep.Timescale,
			rep.Start.DecimalFixed(6), rep.End.DecimalFixed(6),
			rep.Duration.DecimalFixed(6), rep.DeltaToLongestMs.DecimalFixed(3))
		fmt.Fprintf(b, "  template media=%s init=%s start_number=%d uses_time=%t\n",
			rep.Template.Media, rep.Template.Initialization,
			rep.Template.StartNumber, rep.Template.UsesTime)
		fmt.Fprintf(b, "  timeline_coverage_tick=%d\n", rep.Template.CoverageTick)
		for _, e := range rep.Template.Timeline {
			tLabel := "-"
			if e.HasT {
				tLabel = fmt.Sprintf("%d", e.T)
			}
			fmt.Fprintf(b, "  segment number=%d t=%s d=%d media=%s\n",
				e.Number, tLabel, e.D, expandMedia(rep, e))
		}
	}
}

func writeSwitchPoints(b *strings.Builder, m *model.Manifest) {
	for i, sp := range m.SwitchPoints {
		ids := make([]string, 0, len(sp.BoundaryTick))
		for id := range sp.BoundaryTick {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf("%s:tick=%d", id, sp.BoundaryTick[id]))
		}
		fmt.Fprintf(b, "switch_point[%d] time_s=%s skew_ms=%s %s\n",
			i, sp.Time.DecimalFixed(6), sp.SkewMs.DecimalFixed(3),
			strings.Join(parts, " "))
	}
}

func writeRejections(b *strings.Builder, m *model.Manifest, loose bool) {
	if len(m.Rejections) == 0 {
		return
	}
	mode := "strict"
	if loose {
		mode = "loose-diagnostic"
	}
	fmt.Fprintf(b, "rejections mode=%s count=%d\n", mode, len(m.Rejections))
	for i, r := range m.Rejections {
		fmt.Fprintf(b, "  rejection[%d] reason=%s representation=%s tick=%d time_s=%s skew_ms=%s\n",
			i, r.Reason, r.Representation, r.Tick,
			r.Time.DecimalFixed(6), r.SkewMs.DecimalFixed(3))
		fmt.Fprintf(b, "    detail=%s\n", r.Detail)
	}
}

func orderedReps(m *model.Manifest) []*model.Representation {
	var reps []*model.Representation
	for _, as := range m.Period.AdaptationSets {
		reps = append(reps, as.Representations...)
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].ID < reps[j].ID })
	return reps
}

// expandMedia resolves $Number$/$Time$ placeholders for one entry so the
// report shows concrete segment references, not a template skeleton.
func expandMedia(rep *model.Representation, e model.SegmentTimelineEntry) string {
	media := rep.Template.Media
	if rep.Template.UsesTime {
		return strings.ReplaceAll(media, "$Time$", fmt.Sprintf("%d", e.T))
	}
	return strings.ReplaceAll(media, "$Number$", fmt.Sprintf("%d", e.Number))
}
