// Package merge joins multiple renditions of one asset onto the common
// timeline produced by the timeline package. It aligns every version's track
// segments by time, itemises missing coverage per version, and reports a
// conflict when the same interval carries different summaries. A conflict is
// resolved only when the manifest explicitly ranks every competing version,
// and the selected version is always recorded.
package merge

import (
	"errors"
	"fmt"
	"sort"

	"mediapipe/internal/media"
	"mediapipe/internal/model"
	"mediapipe/internal/timeline"
)

// Version is one rendition's mapped tracks together with its optional rank.
type Version struct {
	ID       string
	Priority *int
	Tracks   []timeline.MappedTrack
}

// HasPriority reports whether the manifest ranked this version.
func (v Version) HasPriority() bool { return v.Priority != nil }

// Interval is one merged time interval of one track.
type Interval struct {
	Span     model.Span
	TrackID  string
	Summary  string
	Selected string // version whose summary was selected
	Reason   string // "single", "agreement" or "priority"
	Present  []string
	Absent   []string
	// MixedBoundaries is true when the covering versions sliced this region
	// at different points even though their content agreed.
	MixedBoundaries bool
	// Cells are the selected version's content digests for the interval.
	Cells []string
}

// MissingItem names a version that does not cover an interval.
type MissingItem struct {
	TrackID string
	Version string
	Span    model.Span
	Reason  string // "track_absent" or "interval_absent"
}

// VersionSummary records one version's view of an interval.
type VersionSummary struct {
	Version string
	Summary string
	Start   model.Nanos
	End     model.Nanos
	HasPrio bool
	Prio    int
}

// Conflict is an interval where versions disagree on content.
type Conflict struct {
	TrackID  string
	Span     model.Span
	Kind     string // "summary_mismatch"
	Details  []VersionSummary
	Resolved bool
	Chosen   string
	Reason   string
}

// BoundaryDivergence records an interval whose covering versions agree on the
// summary but split it at different points. It is informational, not a
// conflict.
type BoundaryDivergence struct {
	TrackID  string
	Span     model.Span
	Versions []string
}

// TrackResult is the merged form of one track.
type TrackResult struct {
	TrackID   string
	Kind      string
	Timescale uint32
	Intervals []Interval
	// Extent is the union of every version's coverage for this track.
	Extent model.Span
	// Common is the part of the extent shared with every other track.
	Common model.Span
}

// Result is the merge output for one asset.
type Result struct {
	Asset      string
	Tracks     []TrackResult
	Missing    []MissingItem
	Conflicts  []Conflict
	Divergence []BoundaryDivergence
	// CommonSpan is the time range covered by every track of every version.
	CommonSpan model.Span
	HasCommon  bool
}

// MissingCount is the number of itemised missing entries.
func (r *Result) MissingCount() int { return len(r.Missing) }

// ConflictCount is the number of conflicting intervals, resolved or not.
func (r *Result) ConflictCount() int { return len(r.Conflicts) }

// UnresolvedCount is the number of conflicts policy could not settle.
func (r *Result) UnresolvedCount() int {
	n := 0
	for _, c := range r.Conflicts {
		if !c.Resolved {
			n++
		}
	}
	return n
}

// ByID returns the merged track with the given id.
func (r *Result) ByID(trackID string) (TrackResult, bool) {
	for _, t := range r.Tracks {
		if t.TrackID == trackID {
			return t, true
		}
	}
	return TrackResult{}, false
}

// Err returns a structured conflict error listing every unresolved conflict,
// or nil when the merge is fully settled.
func (r *Result) Err() error {
	errs := make([]*model.Error, 0, r.UnresolvedCount())
	for _, c := range r.Conflicts {
		if c.Resolved {
			continue
		}
		cause := fmt.Errorf("%s on %s track %s: %s", c.Kind, c.Span, c.TrackID, describe(c))
		errs = append(errs, model.New(model.KindConflict, model.StageMerge,
			r.Asset, "merge", cause).WithTrack(c.TrackID))
	}
	if len(errs) == 0 {
		return nil
	}
	return &model.MultiError{Kind: model.KindConflict, Label: "unresolved merge conflicts", Errors: errs}
}

func describe(c Conflict) string {
	out := "versions disagree:"
	for _, d := range c.Details {
		out += fmt.Sprintf(" %s=%s[%s,%s)", d.Version, d.Summary, d.Start, d.End)
	}
	return out
}

// Merge combines the versions for one asset.
func Merge(asset string, versions []Version) (*Result, error) {
	if len(versions) == 0 {
		return nil, model.New(model.KindInvalid, model.StageMerge, asset, "merge",
			errors.New("no versions to merge"))
	}
	seen := make(map[string]bool, len(versions))
	for _, v := range versions {
		if v.ID == "" {
			return nil, model.New(model.KindInvalid, model.StageMerge, asset, "merge",
				errors.New("version with empty id"))
		}
		if seen[v.ID] {
			return nil, model.New(model.KindInvalid, model.StageMerge, asset, "merge",
				fmt.Errorf("duplicate version id %q", v.ID))
		}
		seen[v.ID] = true
	}
	res := &Result{Asset: asset}
	for _, trackID := range collectTrackIDs(versions) {
		tr, err := mergeTrack(asset, trackID, versions)
		if err != nil {
			return nil, err
		}
		res.Tracks = append(res.Tracks, tr.TrackResult)
		res.Missing = append(res.Missing, tr.missing...)
		res.Conflicts = append(res.Conflicts, tr.conflicts...)
		res.Divergence = append(res.Divergence, tr.divergence...)
	}
	res.Missing = coalesceMissing(res.Missing)
	computeCommon(res)
	return res, nil
}

type mergedTrack struct {
	TrackResult
	missing    []MissingItem
	conflicts  []Conflict
	divergence []BoundaryDivergence
}

// versionTrack pairs a version with the track being merged.
type versionTrack struct {
	version Version
	track   timeline.MappedTrack
}

func collectTrackIDs(versions []Version) []string {
	set := map[string]bool{}
	for _, v := range versions {
		for _, t := range v.Tracks {
			set[t.TrackID] = true
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return lessTrackID(ids[i], ids[j]) })
	return ids
}

func lessTrackID(a, b string) bool {
	na, oka := parseUint(a)
	nb, okb := parseUint(b)
	if oka && okb && na != nb {
		return na < nb
	}
	return a < b
}

func parseUint(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + uint64(r-'0')
	}
	return n, true
}

func mergeTrack(asset, trackID string, versions []Version) (mergedTrack, error) {
	out := mergedTrack{TrackResult: TrackResult{TrackID: trackID}}

	var present []versionTrack
	var trackAbsent []string
	for _, v := range versions {
		found := false
		for _, t := range v.Tracks {
			if t.TrackID != trackID {
				continue
			}
			present = append(present, versionTrack{version: v, track: t})
			out.Kind = t.Kind
			out.Timescale = t.Timescale
			found = true
			break
		}
		if !found {
			trackAbsent = append(trackAbsent, v.ID)
		}
	}
	if len(present) == 0 {
		return out, nil
	}
	for i := 1; i < len(present); i++ {
		if present[i].track.Kind != present[0].track.Kind {
			return out, model.New(model.KindConflict, model.StageMerge, asset, "merge",
				fmt.Errorf("track %s kind %q vs %q", trackID,
					present[0].track.Kind, present[i].track.Kind)).
				WithTrack(trackID).WithVersion(present[i].version.ID)
		}
	}

	bounds := boundarySet(present)
	if len(bounds) < 2 {
		return out, nil
	}
	sort.Strings(trackAbsent)
	for _, id := range trackAbsent {
		out.missing = append(out.missing, MissingItem{
			TrackID: trackID, Version: id,
			Span:   model.Span{Start: bounds[0], End: bounds[len(bounds)-1]},
			Reason: "track_absent",
		})
	}

	for i := 0; i+1 < len(bounds); i++ {
		a, b := bounds[i], bounds[i+1]
		if b > a {
			out.mergeInterval(trackID, model.Span{Start: a, End: b}, present)
		}
	}

	out.Intervals = coalesce(out.Intervals)
	if len(out.Intervals) > 0 {
		out.Extent = model.Span{
			Start: out.Intervals[0].Span.Start,
			End:   out.Intervals[len(out.Intervals)-1].Span.End,
		}
	}
	return out, nil
}

// mergeInterval decides one atomic interval between consecutive boundaries.
//
// Content agreement is decided by comparing grid cells, not coarse range
// summaries: two versions that publish identical media under different range
// boundaries carry the same cells and therefore agree. A coarse summary is
// only consulted when no cell data is available (hand-written manifests).
func (o *mergedTrack) mergeInterval(trackID string, span model.Span, present []versionTrack) {
	var covering []versionTrack
	var absent []string
	for _, p := range present {
		if _, ok := coveringSegment(p.track, span); ok {
			covering = append(covering, p)
		} else {
			absent = append(absent, p.version.ID)
		}
	}
	if len(covering) == 0 {
		return
	}
	sort.Strings(absent)
	for _, id := range absent {
		o.missing = append(o.missing, MissingItem{
			TrackID: trackID, Version: id, Span: span, Reason: "interval_absent",
		})
	}

	agree, mixed := contentAgrees(covering, span)
	if agree {
		sel, ranked := selectVersion(covering)
		reason := "agreement"
		switch {
		case len(covering) == 1:
			reason = "single"
		case ranked:
			reason = "priority"
		}
		chosen := sel
		if len(covering) == 1 && !ranked {
			chosen = covering[0]
		}
		o.Intervals = append(o.Intervals, Interval{
			Span: span, TrackID: trackID, Summary: summaryAt(sel.track, span),
			Selected: sel.version.ID, Reason: reason,
			Present: presentIDs(covering), Absent: absent, MixedBoundaries: mixed,
			Cells: cellsAt(sel.track, span),
		})
		if mixed {
			o.divergence = append(o.divergence, BoundaryDivergence{
				TrackID: trackID, Span: span, Versions: presentIDs(covering),
			})
		}
		_ = chosen
		return
	}

	conflict := Conflict{
		TrackID: trackID, Span: span, Kind: "summary_mismatch",
		Details: conflictDetails(covering, span),
	}
	chosen, ok := chooseByPriority(covering)
	if ok {
		conflict.Resolved = true
		conflict.Chosen = chosen.version.ID
		conflict.Reason = "explicit priority"
		o.Intervals = append(o.Intervals, Interval{
			Span: span, TrackID: trackID, Summary: summaryAt(chosen.track, span),
			Selected: chosen.version.ID, Reason: "priority",
			Present: presentIDs(covering), Absent: absent, MixedBoundaries: mixed,
			Cells: cellsAt(chosen.track, span),
		})
	} else {
		conflict.Reason = "no explicit priority to select a version"
		o.Intervals = append(o.Intervals, Interval{
			Span: span, TrackID: trackID, Reason: "unresolved",
			Present: presentIDs(covering), Absent: absent,
		})
	}
	o.conflicts = append(o.conflicts, conflict)
}

// contentAgrees compares the grid cells every covering version supplies for
// the interval. mixed reports that the versions' own segment boundaries
// differ inside the interval.
func contentAgrees(ps []versionTrack, span model.Span) (agree, mixed bool) {
	var reference []string
	haveCells := false
	boundaries := make([][2]model.Nanos, 0, len(ps))
	for _, p := range ps {
		seg, ok := coveringSegment(p.track, span)
		if !ok {
			return false, false
		}
		boundaries = append(boundaries, [2]model.Nanos{seg.Timeline.Start, seg.Timeline.End})
		cells, ok := seg.CellSlice(span.Start, span.End)
		if !ok {
			// No cell data: fall back to the coarse summary for this pair.
			continue
		}
		if !haveCells {
			reference = cells
			haveCells = true
			continue
		}
		if !sameCells(reference, cells) {
			return false, false
		}
	}
	for i := 1; i < len(boundaries); i++ {
		if boundaries[i] != boundaries[0] {
			mixed = true
			break
		}
	}
	if haveCells {
		// Every covering version that has cell data agreed; versions without
		// cell data are compared on the coarse summary instead.
		for _, p := range ps {
			seg, _ := coveringSegment(p.track, span)
			if _, ok := seg.CellSlice(span.Start, span.End); ok {
				continue
			}
			if reference != nil && summaryAt(p.track, span) != summaryOfCells(reference) {
				return false, mixed
			}
		}
		return true, mixed
	}
	first := summaryAt(ps[0].track, span)
	for _, p := range ps[1:] {
		if summaryAt(p.track, span) != first {
			return false, mixed
		}
	}
	return true, mixed
}

func sameCells(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// summaryOfCells reduces cells the same way the manifest does.
func summaryOfCells(cells []string) string {
	return media.CombineChunks(cells)
}

// cellsAt returns the cells of t covering span, or nil when unavailable.
func cellsAt(t timeline.MappedTrack, span model.Span) []string {
	seg, ok := coveringSegment(t, span)
	if !ok {
		return nil
	}
	cells, ok := seg.CellSlice(span.Start, span.End)
	if !ok {
		return nil
	}
	return append([]string(nil), cells...)
}

// summaryAt returns the coarse summary of the covering segment.
func summaryAt(t timeline.MappedTrack, span model.Span) string {
	if s, ok := coveringSegment(t, span); ok {
		return s.Summary
	}
	return ""
}

// presentIDs returns the sorted ids of the given versions.
func presentIDs(ps []versionTrack) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.version.ID
	}
	sort.Strings(out)
	return out
}

// conflictDetails lists each version's view of the conflicting interval.
func conflictDetails(ps []versionTrack, span model.Span) []VersionSummary {
	out := make([]VersionSummary, 0, len(ps))
	for _, p := range ps {
		seg, ok := coveringSegment(p.track, span)
		vs := VersionSummary{Version: p.version.ID, Summary: seg.Summary,
			Start: seg.Timeline.Start, End: seg.Timeline.End}
		if !ok {
			vs.Summary = ""
		}
		if p.version.HasPriority() {
			vs.HasPrio = true
			vs.Prio = *p.version.Priority
		}
		out = append(out, vs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// coveringSegment returns the segment that fully contains span.
func coveringSegment(t timeline.MappedTrack, span model.Span) (timeline.MappedSegment, bool) {
	for _, s := range t.Segments {
		if s.Timeline.Start <= span.Start && s.Timeline.End >= span.End {
			return s, true
		}
	}
	return timeline.MappedSegment{}, false
}

// selectVersion picks a deterministic version to label an agreeing interval:
// the unique highest explicit priority when every candidate is ranked, and
// otherwise the lexicographically smallest version id. The second result says
// whether an explicit ranking decided it.
func selectVersion(ps []versionTrack) (versionTrack, bool) {
	if v, ok := chooseByPriority(ps); ok {
		return v, true
	}
	best := ps[0]
	for _, p := range ps[1:] {
		if p.version.ID < best.version.ID {
			best = p
		}
	}
	return best, false
}

// chooseByPriority selects the unique highest-priority option. Every candidate
// must be explicitly ranked: a partially ranked set has no defined import, so
// no selection is made.
func chooseByPriority(ps []versionTrack) (versionTrack, bool) {
	var best versionTrack
	found := false
	for _, p := range ps {
		if !p.version.HasPriority() {
			return versionTrack{}, false
		}
		if !found || *p.version.Priority > *best.version.Priority {
			best = p
			found = true
		}
	}
	if !found {
		return versionTrack{}, false
	}
	ties := 0
	for _, p := range ps {
		if *p.version.Priority == *best.version.Priority {
			ties++
		}
	}
	if ties != 1 {
		return versionTrack{}, false
	}
	return best, true
}

func boundarySet(present []versionTrack) []model.Nanos {
	seen := map[model.Nanos]bool{}
	for _, p := range present {
		for _, s := range p.track.Segments {
			seen[s.Timeline.Start] = true
			seen[s.Timeline.End] = true
		}
	}
	out := make([]model.Nanos, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func coalesce(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	out := make([]Interval, 0, len(in))
	for _, iv := range in {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.TrackID == iv.TrackID && last.Selected == iv.Selected &&
				last.Summary == iv.Summary && last.Reason == iv.Reason &&
				last.Span.End == iv.Span.Start {
				last.Span.End = iv.Span.End
				last.MixedBoundaries = last.MixedBoundaries || iv.MixedBoundaries
				last.Present = unionSorted(last.Present, iv.Present)
				last.Absent = intersectSorted(last.Absent, iv.Absent)
				continue
			}
		}
		cp := iv
		cp.Present = append([]string(nil), iv.Present...)
		cp.Absent = append([]string(nil), iv.Absent...)
		out = append(out, cp)
	}
	return out
}

// coalesceMissing merges contiguous missing spans of the same version/reason
// so a version lacking a whole region is reported once, not once per slice.
func coalesceMissing(in []MissingItem) []MissingItem {
	if len(in) == 0 {
		return nil
	}
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].TrackID != in[j].TrackID {
			return lessTrackID(in[i].TrackID, in[j].TrackID)
		}
		if in[i].Version != in[j].Version {
			return in[i].Version < in[j].Version
		}
		return in[i].Span.Start < in[j].Span.Start
	})
	out := make([]MissingItem, 0, len(in))
	for _, m := range in {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.TrackID == m.TrackID && last.Version == m.Version &&
				last.Reason == m.Reason && last.Span.End == m.Span.Start {
				last.Span.End = m.Span.End
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func unionSorted(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func intersectSorted(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if set[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// computeCommon intersects every track's extent.
func computeCommon(res *Result) {
	if len(res.Tracks) == 0 {
		res.HasCommon = false
		return
	}
	common := res.Tracks[0].Extent
	for _, t := range res.Tracks[1:] {
		in, ok := common.Intersect(t.Extent)
		if !ok {
			res.HasCommon = false
			return
		}
		common = in
	}
	res.CommonSpan = common
	res.HasCommon = common.End > common.Start
	for i := range res.Tracks {
		res.Tracks[i].Common = common
	}
}
