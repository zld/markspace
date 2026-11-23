package timeline

import (
	"sort"

	"mediapack/internal/model"
)

// MasterTimeline is the merged, time-aligned view of every track.
type MasterTimeline struct {
	Tracks       []*TrackTimeline `json:"tracks"`
	StartNS      int64            `json:"start_ns"`
	EndNS        int64            `json:"end_ns"`
	CommonPoints []int64          `json:"common_decodable_ns"`
	MasterTrack  uint32           `json:"master_track_id"`
}

// Align expands every clock table onto one master nanosecond timeline,
// estimates drift, and computes the set of common decodable points: master
// timestamps at which every track has a sample (duplicate timestamps count).
// The longest track is chosen as the master clock deterministically.
func Align(tables []*ClockTable, nominalNS map[uint32]int64, es *model.Errors) (*MasterTimeline, error) {
	if len(tables) == 0 {
		es.Newf(model.StageTimeline, model.CodeMissingTrack, "", 0,
			model.ByteRange{}, "no tracks to align")
		return nil, es.Err()
	}
	mt := &MasterTimeline{}
	perTrackTimes := make(map[uint32][]int64, len(tables))
	for _, ct := range tables {
		tl, err := ct.Expand(es)
		if err != nil && es.Len() == 0 {
			return nil, err
		}
		if tl == nil {
			continue
		}
		nom := int64(0)
		if nominalNS != nil {
			nom = nominalNS[ct.TrackID]
		}
		drift, derr := EstimateDrift(tl, nom)
		if derr != nil {
			es.Newf(model.StageTimeline, model.CodeOverflow, "", ct.TrackID,
				model.ByteRange{}, "drift overflow: %v", derr)
			return nil, es.Err()
		}
		tl.Drift = drift
		mt.Tracks = append(mt.Tracks, tl)
		times := make([]int64, 0, len(tl.Events))
		for _, ev := range tl.Events {
			times = append(times, ev.MasterNS)
		}
		perTrackTimes[ct.TrackID] = times
	}
	if es.Len() > 0 {
		return nil, es.Err()
	}
	sort.Slice(mt.Tracks, func(i, j int) bool {
		if len(mt.Tracks[i].Events) != len(mt.Tracks[j].Events) {
			return len(mt.Tracks[i].Events) > len(mt.Tracks[j].Events)
		}
		return mt.Tracks[i].TrackID < mt.Tracks[j].TrackID
	})
	mt.MasterTrack = mt.Tracks[0].TrackID
	mt.StartNS = mt.Tracks[0].Events[0].MasterNS
	mt.EndNS = mt.Tracks[0].LastMasterNS
	mt.CommonPoints = commonPoints(perTrackTimes)
	return mt, nil
}

// commonPoints returns the sorted intersection of per-track master timestamp
// sets. These are the only legal segment boundaries: every track can begin
// decoding at each returned point.
func commonPoints(perTrack map[uint32][]int64) []int64 {
	keys := make([]uint32, 0, len(perTrack))
	for k := range perTrack {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	counts := make(map[int64]int)
	var union []int64
	for _, k := range keys {
		seen := make(map[int64]struct{})
		for _, t := range perTrack[k] {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				counts[t]++
				if counts[t] == 1 {
					union = append(union, t)
				}
			}
		}
	}
	sort.Slice(union, func(i, j int) bool { return union[i] < union[j] })
	want := len(keys)
	out := make([]int64, 0)
	for _, t := range union {
		if counts[t] == want {
			out = append(out, t)
		}
	}
	return out
}

// BoundaryAt returns the common decodable point nearest to targetNS that is
// >= targetNS when preferAfter is true, otherwise the nearest point overall.
// ok is false when no candidate exists.
func (m *MasterTimeline) BoundaryAt(targetNS int64, preferAfter bool) (ns int64, ok bool) {
	if len(m.CommonPoints) == 0 {
		return 0, false
	}
	idx := sort.Search(len(m.CommonPoints), func(i int) bool {
		return m.CommonPoints[i] >= targetNS
	})
	if preferAfter {
		if idx >= len(m.CommonPoints) {
			return m.CommonPoints[len(m.CommonPoints)-1], true
		}
		return m.CommonPoints[idx], true
	}
	if idx >= len(m.CommonPoints) {
		return m.CommonPoints[len(m.CommonPoints)-1], true
	}
	after := m.CommonPoints[idx]
	if idx == 0 {
		return after, true
	}
	before := m.CommonPoints[idx-1]
	if targetNS-before <= after-targetNS {
		return before, true
	}
	return after, true
}
