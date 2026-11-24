// Package timeline turns explicit clip in/out points and declared transitions
// into ordered output samples on one rational time base.
package timeline

import (
	"fmt"

	"mediapack/internal/clock"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/maptrack"
)

type ClipSpec struct {
	ID       string         `json:"id"`
	TargetID uint32         `json:"target_id"`
	Group    string         `json:"group,omitempty"`
	Start    clock.Rational `json:"start"`
	In       clock.Rational `json:"in"`
	Out      clock.Rational `json:"out"`
}

type TransitionSpec struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	FromClipID string         `json:"from_clip"`
	ToClipID   string         `json:"to_clip"`
	Overlap    clock.Rational `json:"overlap"`
}

type Spec struct {
	Clips       []ClipSpec       `json:"clips"`
	Transitions []TransitionSpec `json:"transitions,omitempty"`
}

type PlannedSample struct {
	TargetID    uint32 `json:"target_id"`
	SourceIndex int    `json:"source_index"`
	DTS         int64  `json:"dts"`
	PTS         int64  `json:"pts"`
	Duration    int64  `json:"duration"`
	Size        int    `json:"size"`
	Offset      int64  `json:"source_offset"`
	Hash        string `json:"payload_hash"`
	Payload     []byte `json:"-"`
}

type ClipResult struct {
	ID          string         `json:"id"`
	TargetID    uint32         `json:"target_id"`
	Start       clock.Rational `json:"start"`
	In          clock.Rational `json:"in"`
	Out         clock.Rational `json:"out"`
	End         clock.Rational `json:"end"`
	SampleCount int            `json:"sample_count"`
	FirstDTS    int64          `json:"first_dts"`
	LastDTS     int64          `json:"last_dts"`
}

type TransitionResult struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	From    string         `json:"from_clip"`
	To      string         `json:"to_clip"`
	Start   clock.Rational `json:"start"`
	End     clock.Rational `json:"end"`
	Overlap clock.Rational `json:"overlap"`
}

type TrackPlan struct {
	TargetID uint32          `json:"target_id"`
	Timebase clock.Timebase  `json:"timebase"`
	Clips    []ClipResult    `json:"clips"`
	Samples  []PlannedSample `json:"samples"`
}

type Timeline struct {
	Timebase    clock.Timebase     `json:"timebase"`
	Tracks      []*TrackPlan       `json:"tracks"`
	Transitions []TransitionResult `json:"transitions,omitempty"`
	Duration    clock.Rational     `json:"duration"`
}

type clipInterval struct {
	spec  ClipSpec
	end   clock.Rational
	group string
}

// Assemble validates edits and overlaps, applies half-open [In, Out) sample
// selection, and converts all timestamps to the mapping table's common base.
func Assemble(table *maptrack.Table, spec Spec) (*Timeline, error) {
	if table == nil {
		return nil, fault.New(fault.KindTimeline, "nil mapping table")
	}
	tl := &Timeline{Timebase: table.CommonTimebase}
	byTarget := map[uint32]*maptrack.MappedTrack{}
	for _, mt := range table.Tracks {
		byTarget[mt.TargetID] = mt
		tl.Tracks = append(tl.Tracks, &TrackPlan{TargetID: mt.TargetID, Timebase: table.CommonTimebase})
	}

	intervals := map[string]clipInterval{}
	for _, c := range spec.Clips {
		mt := byTarget[c.TargetID]
		if mt == nil {
			return nil, fault.AtPosition(fault.KindTimeline, clipPos(c), "unknown target track %d", c.TargetID)
		}
		if c.Start.Num < 0 || c.In.Num < 0 || c.Out.Num < 0 {
			return nil, fault.AtPosition(fault.KindTimeline, clipPos(c), "negative timeline or source point")
		}
		dur := c.Out.Sub(c.In)
		if dur.Num <= 0 {
			return nil, fault.AtPosition(fault.KindTimeline, clipPos(c), "clip duration must be positive")
		}
		end := c.Start.Add(dur)
		group := c.Group
		if group == "" {
			group = fmt.Sprintf("target-%d", c.TargetID)
		}
		intervals[c.ID] = clipInterval{spec: c, end: end, group: group}
	}

	transResults, err := validateOverlaps(spec.Transitions, intervals)
	if err != nil {
		return nil, err
	}
	tl.Transitions = transResults

	var duration clock.Rational
	for _, c := range spec.Clips {
		mt := byTarget[c.TargetID]
		plan := trackPlan(tl, c.TargetID)
		result, samples, err := buildClipSamples(mt, c, tl.Timebase)
		if err != nil {
			return nil, err
		}
		if n := len(plan.Samples); n > 0 && len(samples) > 0 && samples[0].DTS < plan.Samples[n-1].DTS {
			return nil, fault.AtPosition(fault.KindTimeline, clipPos(c),
				"backward DTS: %d after %d", samples[0].DTS, plan.Samples[n-1].DTS)
		}
		plan.Clips = append(plan.Clips, result)
		plan.Samples = append(plan.Samples, samples...)
		if result.End.Cmp(duration) > 0 {
			duration = result.End
		}
	}
	tl.Duration = duration
	return tl, nil
}

func buildClipSamples(mt *maptrack.MappedTrack, c ClipSpec, common clock.Timebase) (ClipResult, []PlannedSample, error) {
	src := mt.Samples()
	var selected []*sampleWithTime
	for _, s := range src {
		t := clock.Ticks(s.DTS, mt.Timebase)
		if t.Cmp(c.In) >= 0 && t.Cmp(c.Out) < 0 {
			selected = append(selected, &sampleWithTime{s: s, t: t})
		}
	}
	res := ClipResult{ID: c.ID, TargetID: c.TargetID, Start: c.Start, In: c.In, Out: c.Out,
		End: c.Start.Add(c.Out.Sub(c.In)), SampleCount: len(selected)}
	if len(selected) == 0 {
		return res, nil, fault.AtPosition(fault.KindTimeline, clipPos(c), "half-open interval contains no samples")
	}
	first := selected[0].s
	startTick := clock.ToTicks(c.Start, common)
	var out []PlannedSample
	for _, sw := range selected {
		s := sw.s
		dtsDelta, err := clock.ExactTicks(s.DTS-first.DTS, mt.Timebase, common)
		if err != nil {
			return res, nil, fault.AtPosition(fault.KindTimeline, clipPos(c), "DTS conversion: %v", err)
		}
		ptsDelta, err := clock.ExactTicks(s.PTS-first.PTS, mt.Timebase, common)
		if err != nil {
			// Composition offset conversion is exact for the chosen LCM base.
			ptsDelta = clock.ConvertTicks(s.PTS-first.PTS, mt.Timebase, common)
		}
		dur, err := clock.ExactTicks(s.Duration, mt.Timebase, common)
		if err != nil {
			dur = clock.ConvertTicks(s.Duration, mt.Timebase, common)
		}
		ps := PlannedSample{
			TargetID: c.TargetID, SourceIndex: s.Index, DTS: startTick + dtsDelta,
			PTS: startTick + ptsDelta, Duration: dur, Size: s.Size, Offset: s.Offset,
			Hash: s.Hash, Payload: s.Payload,
		}
		if len(out) > 0 && ps.DTS < out[len(out)-1].DTS {
			return res, nil, fault.AtPosition(fault.KindTimeline, clipPos(c), "backward DTS within clip")
		}
		out = append(out, ps)
	}
	res.FirstDTS = out[0].DTS
	res.LastDTS = out[len(out)-1].DTS
	return res, out, nil
}

type sampleWithTime struct {
	s *demux.Sample
	t clock.Rational
}

func trackPlan(tl *Timeline, id uint32) *TrackPlan {
	for _, p := range tl.Tracks {
		if p.TargetID == id {
			return p
		}
	}
	return nil
}

func validateOverlaps(trans []TransitionSpec, clips map[string]clipInterval) ([]TransitionResult, error) {
	declared := map[string]TransitionSpec{}
	for _, tr := range trans {
		if tr.Overlap.Num <= 0 {
			return nil, fault.AtPosition(fault.KindTimeline, tr.ID, "transition overlap must be positive")
		}
		declared[pairKey(tr.FromClipID, tr.ToClipID)] = tr
	}
	var results []TransitionResult
	seenPair := map[string]bool{}
	for _, tr := range trans {
		a, ok1 := clips[tr.FromClipID]
		b, ok2 := clips[tr.ToClipID]
		if !ok1 || !ok2 {
			return nil, fault.AtPosition(fault.KindTimeline, tr.ID, "transition references unknown clip")
		}
		start := maxRat(a.spec.Start, b.spec.Start)
		end := minRat(a.end, b.end)
		got := end.Sub(start)
		if got.Cmp(tr.Overlap) != 0 || got.Num <= 0 {
			return nil, fault.AtPosition(fault.KindTimeline, tr.ID,
				"declared overlap %s does not match calculated overlap %s", tr.Overlap, got)
		}
		results = append(results, TransitionResult{
			ID: tr.ID, Kind: tr.Kind, From: tr.FromClipID, To: tr.ToClipID,
			Start: start, End: end, Overlap: got,
		})
		seenPair[pairKey(tr.FromClipID, tr.ToClipID)] = true
	}

	ids := make([]string, 0, len(clips))
	for id := range clips {
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a := clips[ids[i]]
			b := clips[ids[j]]
			if a.group != b.group {
				continue // independent A/V groups may overlap without a transition
			}
			start := maxRat(a.spec.Start, b.spec.Start)
			end := minRat(a.end, b.end)
			if end.Cmp(start) <= 0 {
				continue
			}
			key := pairKey(ids[i], ids[j])
			reverse := pairKey(ids[j], ids[i])
			if !seenPair[key] && !seenPair[reverse] {
				return nil, fault.AtPosition(fault.KindTimeline, ids[i]+"/"+ids[j],
					"undeclared overlap of %s", end.Sub(start))
			}
		}
	}
	return results, nil
}

func pairKey(a, b string) string { return a + "|" + b }
func clipPos(c ClipSpec) string  { return fmt.Sprintf("clip=%s,target=%d", c.ID, c.TargetID) }
func maxRat(a, b clock.Rational) clock.Rational {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}
func minRat(a, b clock.Rational) clock.Rational {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}
