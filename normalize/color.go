package normalize

import (
	"fmt"
	"sort"

	"mediadist/timeline"
)

// ColorDecision records the outcome of color arbitration for one video track.
type ColorDecision struct {
	TrackID  uint32
	Resolved bool
	Pending  bool // no metadata available and no CLI override supplied
	Source   timeline.ColorSource
	Desc     timeline.ColorDesc
	Conflict bool
}

// defaultPriority ranks sources when no explicit CLI order is supplied.
var defaultPriority = []timeline.ColorSource{
	timeline.SourceCLI,
	timeline.SourceBitstream,
	timeline.SourceContainer,
}

// ArbitrateColor resolves the color description for a video track.
//
//   - A CLI override always wins (it is the operator's explicit adjudication).
//   - Otherwise candidates are ranked by priority; equal-valued candidates from
//     different sources are not a conflict.
//   - Genuinely conflicting values produce a COLOR_CONFLICT error naming both
//     the track and the competing sources.
//   - With no candidates at all the decision stays pending, which blocks
//     manifest publication downstream.
func ArbitrateColor(t *timeline.Track, cli *timeline.ColorDesc,
	priority []timeline.ColorSource) (*ColorDecision, error) {
	if t.Kind != timeline.KindVideo {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageNormalize,
			t.ID, -1, "color", fmt.Errorf("track %d is not video", t.ID))
	}
	dec := &ColorDecision{TrackID: t.ID}
	if cli != nil {
		d := *cli
		d.Source = timeline.SourceCLI
		dec.Resolved, dec.Source, dec.Desc = true, timeline.SourceCLI, d
		t.Color = &d
		return dec, nil
	}
	if len(priority) == 0 {
		priority = defaultPriority
	}
	if len(t.ColorCandidates) == 0 {
		dec.Pending = true
		return dec, nil
	}
	conflictSrc, conflict := detectConflict(t.ColorCandidates)
	if conflict {
		return dec, timeline.NewLocError(timeline.CodeColorConflict, timeline.StageNormalize,
			t.ID, -1, "color-arbitration",
			fmt.Errorf("competing descriptions from %v", conflictSrc))
	}
	// No value conflict: pick the highest-priority available source.
	for _, src := range priority {
		if d, ok := t.ColorCandidates[src]; ok {
			d.Source = src
			dec.Resolved, dec.Source, dec.Desc = true, src, d
			t.Color = &d
			return dec, nil
		}
	}
	dec.Pending = true
	return dec, nil
}

// detectConflict reports whether candidates disagree on color values.
func detectConflict(cands map[timeline.ColorSource]timeline.ColorDesc) ([]timeline.ColorSource, bool) {
	srcs := make([]timeline.ColorSource, 0, len(cands))
	for s := range cands {
		srcs = append(srcs, s)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i] < srcs[j] })
	var ref timeline.ColorDesc
	for i, s := range srcs {
		d := cands[s]
		if i == 0 {
			ref = d
			continue
		}
		if !ref.Equal(d) {
			return srcs, true
		}
	}
	return nil, false
}

// RequireResolved converts a pending color decision into the COLOR_PENDING
// error that blocks manifest publication.
func RequireResolved(dec *ColorDecision) error {
	if dec == nil || !dec.Resolved {
		return timeline.NewLocError(timeline.CodeColorPending, timeline.StageNormalize,
			dec.trackID(), -1, "color-publish", fmt.Errorf("color metadata missing or undecided"))
	}
	return nil
}

func (d *ColorDecision) trackID() uint32 {
	if d == nil {
		return 0
	}
	return d.TrackID
}
