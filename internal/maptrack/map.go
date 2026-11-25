// Package maptrack applies explicit input->output track mapping rules and
// calibrates per-track clock drift on the unified timebase. It never
// touches sample payload bytes.
package maptrack

import (
	"sort"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// Rule maps one input track ID to one output track ID.
type Rule struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

// CalibrateTracks aligns every media/subtitle track's first tick to the
// first tick of the earliest such track, failing when the pre-calibration
// deviation of any track exceeds threshold (unified ticks). Metadata
// pseudo-tracks (emsg/chpl/ID3) keep their absolute timeline. Events are
// shifted in place.
func CalibrateTracks(events []model.Event, tracks []model.Track, threshold int64) ([]clock.DriftReport, error) {
	first := map[string]int64{}
	have := map[string]bool{}
	for _, e := range events {
		if !have[e.TrackID] {
			first[e.TrackID] = e.Ticks
			have[e.TrackID] = true
		}
	}
	reference := int64(-1)
	for _, t := range tracks {
		if t.Kind == model.TrackMeta {
			continue
		}
		if f, ok := first[t.ID]; ok && (reference < 0 || f < reference) {
			reference = f
		}
	}
	if reference < 0 {
		return nil, nil
	}
	var reports []clock.DriftReport
	for _, t := range tracks {
		if t.Kind == model.TrackMeta {
			continue
		}
		f, ok := first[t.ID]
		if !ok {
			continue
		}
		rep := clock.Calibrate(t.ID, reference, f, threshold)
		reports = append(reports, rep)
		if !rep.WithinLimit {
			return reports, model.NewError(model.ErrDrift, model.StageMap, t.ID, -1,
				"drift %d ticks exceeds threshold %d", rep.DevBefore, threshold)
		}
		if rep.Offset != 0 {
			for i := range events {
				if events[i].TrackID == t.ID {
					events[i].Ticks += rep.Offset
				}
			}
		}
	}
	return reports, nil
}

// Apply validates the rule set and assigns OutTrackID on every event.
// The returned slice is sorted deterministically: by presentation ticks,
// then input track ID, then source byte offset, then demux sequence.
func Apply(events []model.Event, tracks []model.Track, rules []Rule, outs []model.OutputTrack) ([]model.Event, error) {
	outByID := map[string]model.OutputTrack{}
	for _, o := range outs {
		if _, dup := outByID[o.ID]; dup {
			return nil, model.NewError(model.ErrMapping, model.StageMap, "", -1, "duplicate output track %q", o.ID)
		}
		outByID[o.ID] = o
	}
	ruleByIn := map[string]string{}
	for _, r := range rules {
		if _, dup := ruleByIn[r.In]; dup {
			return nil, model.NewError(model.ErrMapping, model.StageMap, r.In, -1, "duplicate mapping for input track")
		}
		if _, ok := outByID[r.Out]; !ok {
			return nil, model.NewError(model.ErrMapping, model.StageMap, r.In, -1, "mapping target %q does not exist", r.Out)
		}
		ruleByIn[r.In] = r.Out
	}
	for _, t := range tracks {
		if _, ok := ruleByIn[t.ID]; !ok {
			return nil, model.NewError(model.ErrMapping, model.StageMap, t.ID, -1, "input track has no mapping rule")
		}
	}
	mapped := make([]model.Event, len(events))
	copy(mapped, events)
	for i := range mapped {
		outID, ok := ruleByIn[mapped[i].TrackID]
		if !ok {
			return nil, model.NewError(model.ErrMapping, model.StageMap, mapped[i].TrackID, mapped[i].SrcOffset,
				"event on unmapped track")
		}
		out := outByID[outID]
		if !out.AcceptsKind(mapped[i].Kind) {
			return nil, model.NewError(model.ErrMapping, model.StageMap, mapped[i].TrackID, mapped[i].SrcOffset,
				"event kind %q incompatible with output track %q", mapped[i].Kind, outID)
		}
		mapped[i].OutTrackID = outID
	}
	sort.SliceStable(mapped, func(i, j int) bool {
		a, b := mapped[i], mapped[j]
		if a.Ticks != b.Ticks {
			return a.Ticks < b.Ticks
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if a.SrcOffset != b.SrcOffset {
			return a.SrcOffset < b.SrcOffset
		}
		return a.Seq < b.Seq
	})
	return mapped, nil
}
