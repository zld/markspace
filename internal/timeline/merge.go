// Package timeline merges validated multi-track frames onto the shared
// nanosecond clock. Merging is stable and total: equal timestamps order by
// track id then sequence, and any time regression or overflow is a hard fault,
// never a silent reorder.
package timeline

import (
	"fmt"
	"sort"

	"medialab/internal/model"
)

// Event is one merged timeline entry.
type Event struct {
	Frame model.Frame `json:"frame"`
	// Index is the position in the merged timeline (0-based).
	Index int `json:"index"`
	// TrackIndex is the per-track position (0-based).
	TrackIndex int `json:"trackIndex"`
}

// Timeline is the merged, ordered result.
type Timeline struct {
	Events   []Event
	Tracks   []model.TrackID
	StartNS  model.Timestamp
	EndNS    model.Timestamp
	Warnings model.Faults
}

// MergeOptions tunes merge behaviour.
type MergeOptions struct {
	// RequireMonotonicPerTrack rejects a track whose timestamps regress. The
	// manifest stores frames unsorted so this is a real input check.
	RequireMonotonicPerTrack bool
}

// Merge validates and merges frames. It returns a hard fault for illegal or
// overflowing timestamps and a warning for timestamp regressions that the
// sorter had to repair.
func Merge(frames []model.Frame, opt MergeOptions) (*Timeline, model.Faults, error) {
	if len(frames) == 0 {
		return nil, nil, model.NewFault(model.KindIngest, model.CodeManifestInvalid, 0, 0,
			"timeline has no frames")
	}

	// Validate timestamps and per-track monotonicity before sorting, so an
	// error names the original declaration order.
	perTrack := make(map[model.TrackID][]model.Frame)
	var warnings model.Faults
	for _, f := range frames {
		if err := f.PTS.Validate(); err != nil {
			fault, _ := model.AsFault(err)
			return nil, nil, model.WithFault(fault, f.Track, f.Sequence)
		}
		perTrack[f.Track] = append(perTrack[f.Track], f)
	}
	tracks := make([]model.TrackID, 0, len(perTrack))
	for tr := range perTrack {
		tracks = append(tracks, tr)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })

	for _, tr := range tracks {
		list := perTrack[tr]
		sort.SliceStable(list, func(i, j int) bool { return list[i].Sequence < list[j].Sequence })
		var prev *model.Frame
		for i := range list {
			if prev != nil && list[i].PTS < prev.PTS {
				f := model.Frame{
					Track:    list[i].Track,
					Sequence: list[i].Sequence,
				}
				msg := fmt.Sprintf("timestamp %d ns follows %d ns on sequence %d",
					int64(list[i].PTS), int64(prev.PTS), prev.Sequence)
				warnings = append(warnings, model.NewFault(model.KindIngest,
					model.CodeTimeRegression, f.Track, f.Sequence, msg).RecoverableFault())
				if opt.RequireMonotonicPerTrack {
					return nil, nil, model.NewFault(model.KindIngest, model.CodeTimeRegression,
						f.Track, f.Sequence, msg)
				}
			}
			prev = &list[i]
		}
	}

	ordered := make([]model.Frame, len(frames))
	copy(ordered, frames)
	sort.SliceStable(ordered, func(i, j int) bool {
		return model.LessSortKey(ordered[i], ordered[j])
	})

	tl := &Timeline{Tracks: tracks, Warnings: warnings}
	trackPos := make(map[model.TrackID]int, len(tracks))
	tl.StartNS = ordered[0].PTS
	for i, f := range ordered {
		if f.PTS < tl.StartNS {
			tl.StartNS = f.PTS
		}
		if f.PTS > tl.EndNS {
			tl.EndNS = f.PTS
		}
		tl.Events = append(tl.Events, Event{
			Frame:      f,
			Index:      i,
			TrackIndex: trackPos[f.Track],
		})
		trackPos[f.Track]++
	}
	return tl, warnings, nil
}

// Span returns the media span covered by the timeline.
func (t *Timeline) Span() int64 {
	return int64(t.EndNS - t.StartNS)
}

// TrackFrames returns the merged events of a single track in order.
func (t *Timeline) TrackFrames(track model.TrackID) []Event {
	var out []Event
	for _, e := range t.Events {
		if e.Frame.Track == track {
			out = append(out, e)
		}
	}
	return out
}

// ApplyOffsets rebases every event by the per-track offsets. Offsets are
// applied in a copy so repeated runs with the same input are identical.
func (t *Timeline) ApplyOffsets(offsets map[model.TrackID]int64) (*Timeline, error) {
	out := &Timeline{Tracks: append([]model.TrackID(nil), t.Tracks...), Warnings: t.Warnings}
	events := make([]Event, len(t.Events))
	copy(events, t.Events)
	for i := range events {
		off, ok := offsets[events[i].Frame.Track]
		if !ok || off == 0 {
			events[i].Index = i
			continue
		}
		shifted, err := model.AddNS(int64(events[i].Frame.PTS), off)
		if err != nil {
			f, _ := model.AsFault(err)
			return nil, model.WithFault(f, events[i].Frame.Track, events[i].Frame.Sequence)
		}
		events[i].Frame.PTS = model.Timestamp(shifted)
		events[i].Index = i
	}
	sort.SliceStable(events, func(i, j int) bool {
		return model.LessSortKey(events[i].Frame, events[j].Frame)
	})
	out.Events = events
	out.StartNS = events[0].Frame.PTS
	for _, e := range events {
		if e.Frame.PTS < out.StartNS {
			out.StartNS = e.Frame.PTS
		}
		if e.Frame.PTS > out.EndNS {
			out.EndNS = e.Frame.PTS
		}
	}
	pos := make(map[model.TrackID]int, len(out.Tracks))
	for i := range out.Events {
		tr := out.Events[i].Frame.Track
		out.Events[i].TrackIndex = pos[tr]
		pos[tr]++
	}
	return out, nil
}
