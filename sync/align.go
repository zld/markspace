package sync

import (
	"fmt"
	"sort"

	"mediarc/model"
)

// Align pairs each cue with the nearest anchor at or before its start (falling
// back to the nearest anchor overall) and measures the error.
func (a *Aligner) Align(cues []CueRef, anchors []Anchor) Result {
	sorted := make([]Anchor, len(anchors))
	copy(sorted, anchors)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].TimeNS != sorted[j].TimeNS {
			return sorted[i].TimeNS < sorted[j].TimeNS
		}
		return sorted[i].Label < sorted[j].Label
	})
	result := Result{Pairs: make([]Pair, 0, len(cues))}
	for _, cue := range cues {
		pair := Pair{Cue: cue}
		anchor, ok := nearestAnchor(sorted, cue.StartNS)
		if !ok {
			result.Unanchored++
			issue := model.New(model.CodeAlignmentDrift, stream,
				fmt.Sprintf("cue %d has no anchor to align against", cue.Index))
			if a.cfg.RequireAnchor {
				pair.Issue = issue
				result.Issues = append(result.Issues, issue)
			}
			result.Pairs = append(result.Pairs, pair)
			continue
		}
		pair.Anchor = anchor
		pair.ErrorNS = cue.StartNS - anchor.TimeNS
		pair.AbsError = abs64(pair.ErrorNS)
		pair.WithinTolerance = pair.AbsError <= a.cfg.ToleranceNS
		if pair.AbsError > result.MaxErrorNS {
			result.MaxErrorNS = pair.AbsError
		}
		if pair.WithinTolerance {
			result.WithinTolerance++
		} else {
			result.Drifted++
			issue := model.New(model.CodeAlignmentDrift, stream,
				fmt.Sprintf("cue %d starts %d ns from anchor %q (tolerance %d ns)",
					cue.Index, pair.ErrorNS, anchor.Label, a.cfg.ToleranceNS))
			pair.Issue = issue
			result.Issues = append(result.Issues, issue)
		}
		result.Pairs = append(result.Pairs, pair)
	}
	return result
}

// nearestAnchor returns the closest anchor by absolute distance, preferring the
// earlier one on a tie so the choice is deterministic.
func nearestAnchor(sorted []Anchor, t int64) (Anchor, bool) {
	if len(sorted) == 0 {
		return Anchor{}, false
	}
	// First anchor with TimeNS >= t.
	idx := sort.Search(len(sorted), func(i int) bool { return sorted[i].TimeNS >= t })
	switch {
	case idx == 0:
		return sorted[0], true
	case idx == len(sorted):
		return sorted[len(sorted)-1], true
	}
	before, after := sorted[idx-1], sorted[idx]
	if t-before.TimeNS <= after.TimeNS-t {
		return before, true
	}
	return after, true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
