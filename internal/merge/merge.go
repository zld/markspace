// Package merge combines mapped source tracks into canonical timelines,
// applying deterministic dedupe, conflict and gap rules.
package merge

import (
	"sort"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

// Policy selects how conflicts and validation failures are handled.
type Policy string

const (
	// PolicyStrict rejects the run on the first conflict.
	PolicyStrict Policy = "strict"
	// PolicyMarkGaps records problems as gaps/rejections and continues.
	PolicyMarkGaps Policy = "mark-gaps"
)

// Conflict describes two overlapping cues whose payload differs.
type Conflict struct {
	TargetTrack string
	A           track.Cue
	B           track.Cue
	Reason      string
}

// Result is the merge output for every target track.
type Result struct {
	Timelines  map[string]*track.Track
	Conflicts  []Conflict
	Gaps       []track.Gap
	Rejected   []track.Rejected
	Duplicates int
}

// MappedTrack pairs a mapped track with the rule that produced it.
type MappedTrack struct {
	SourceID  string
	Version   string
	TargetID  string
	Language  string
	Track     *track.Track
}

type interval struct{ start, end clock.Ticks }

// Merge runs the mapping output through the dedupe/conflict/gap rules.
// Inputs are grouped by target track id and processed in stable source order.
func Merge(inputs []MappedTrack, policy Policy) (*Result, error) {
	if policy != PolicyStrict && policy != PolicyMarkGaps {
		return nil, fault.New(fault.StageMerge, fault.KindState, "", "unknown merge policy: "+string(policy))
	}
	res := &Result{Timelines: map[string]*track.Track{}}
	groups := map[string][]MappedTrack{}
	var targets []string
	for _, in := range inputs {
		if _, ok := groups[in.TargetID]; !ok {
			targets = append(targets, in.TargetID)
		}
		groups[in.TargetID] = append(groups[in.TargetID], in)
	}
	sort.Strings(targets)
	for _, tid := range targets {
		grp := groups[tid]
		sort.SliceStable(grp, func(i, j int) bool {
			if grp[i].SourceID != grp[j].SourceID {
				return grp[i].SourceID < grp[j].SourceID
			}
			return grp[i].Version < grp[j].Version
		})
		tl := &track.Track{ID: tid}
		coverage := map[string][]interval{}
		for _, in := range grp {
			if tl.Language == "" {
				tl.Language = in.Language
			}
			for _, c := range in.Track.Cues {
				accept, conflict := placeCue(tl, c)
				// Coverage is credited even for exact duplicates: the source
				// genuinely covered this range, it simply added no new cue.
				coverage[in.SourceID] = append(coverage[in.SourceID], interval{c.Start, c.End})
				if conflict != nil {
					cf := Conflict{TargetTrack: tid, A: conflict.A, B: c,
						Reason: "overlapping cues with different text"}
					res.Conflicts = append(res.Conflicts, cf)
					if policy == PolicyStrict {
						return res, fault.New(fault.StageMerge, fault.KindConflict, tid,
							"strict policy: "+cf.Reason)
					}
					res.Rejected = append(res.Rejected, track.Rejected{
						Source: c.Source, Start: c.Start, End: c.End,
						Reason: cf.Reason})
					continue
				}
				if !accept {
					res.Duplicates++
					continue
				}
				tl.Cues = append(tl.Cues, c)
			}
		}
		sort.SliceStable(tl.Cues, func(i, j int) bool { return tl.Cues[i].Start < tl.Cues[j].Start })
		res.Timelines[tid] = tl
		res.Gaps = append(res.Gaps, coverageGaps(tid, grp, coverage)...)
	}
	return res, nil
}

// placeCue decides whether c is accepted. It returns accept=false for an exact
// duplicate and a non-nil conflict for overlapping cues with differing text.
func placeCue(tl *track.Track, c track.Cue) (accept bool, conflict *struct{ A, B track.Cue }) {
	for _, ex := range tl.Cues {
		if !overlaps(ex.Start, ex.End, c.Start, c.End) {
			continue
		}
		if ex.Start == c.Start && ex.End == c.End && ex.Text == c.Text {
			return false, nil
		}
		return false, &struct{ A, B track.Cue }{A: ex, B: c}
	}
	return true, nil
}

func overlaps(aStart, aEnd, bStart, bEnd clock.Ticks) bool {
	return aStart < bEnd && bStart < aEnd
}

// coverageGaps marks, per source, the parts of the union coverage that the
// source itself does not cover. Every gap carries full provenance.
func coverageGaps(tid string, grp []MappedTrack, coverage map[string][]interval) []track.Gap {
	var union []interval
	for _, ivs := range coverage {
		union = append(union, ivs...)
	}
	union = mergeIntervals(union)
	var gaps []track.Gap
	for _, in := range grp {
		have := mergeIntervals(coverage[in.SourceID])
		for _, u := range union {
			for _, miss := range subtract(u, have) {
				gaps = append(gaps, track.Gap{
					TrackID: tid, Source: in.SourceID, Version: in.Version,
					Start: miss.start, End: miss.end,
					Reason: "source has no cue covering timeline range",
				})
			}
		}
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Source != gaps[j].Source {
			return gaps[i].Source < gaps[j].Source
		}
		return gaps[i].Start < gaps[j].Start
	})
	return gaps
}

func mergeIntervals(in []interval) []interval {
	if len(in) == 0 {
		return nil
	}
	sort.SliceStable(in, func(i, j int) bool { return in[i].start < in[j].start })
	out := []interval{in[0]}
	for _, iv := range in[1:] {
		last := &out[len(out)-1]
		if iv.start < last.end {
			if iv.end > last.end {
				last.end = iv.end
			}
		} else {
			out = append(out, iv)
		}
	}
	return out
}

// subtract returns the parts of want not covered by any interval in have.
func subtract(want interval, have []interval) []interval {
	cursor := want.start
	var out []interval
	for _, h := range have {
		if h.end <= cursor {
			continue
		}
		if h.start >= want.end {
			break
		}
		if h.start > cursor {
			out = append(out, interval{cursor, h.start})
		}
		if h.end > cursor {
			cursor = h.end
		}
	}
	if cursor < want.end {
		out = append(out, interval{cursor, want.end})
	}
	return out
}
