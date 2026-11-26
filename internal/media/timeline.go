package media

import (
	"sort"
)

// EventKind identifies the media type carried by a unified timeline event.
type EventKind string

const (
	KindVideo EventKind = "video"
	KindAudio EventKind = "audio"
	KindCue   EventKind = "cue"
)

// Event is the common nanosecond representation of EBML blocks, RTP packets,
// and source-switch cues.
type Event struct {
	TrackID        uint32    `json:"track_id"`
	Sequence       int       `json:"sequence"`
	GlobalSequence int       `json:"global_sequence"`
	Kind           EventKind `json:"kind"`
	PTSNS          int64     `json:"pts_ns"`
	DurationNS     int64     `json:"duration_ns,omitempty"`
	EndNS          int64     `json:"end_ns,omitempty"`
	SourceID       string    `json:"source_id,omitempty"`
	Offset         int64     `json:"input_offset,omitempty"`
	Lost           bool      `json:"lost,omitempty"`
	Detail         string    `json:"detail,omitempty"`
}

// SourceClock maps one source's raw anchor to the continuous output clock.
type SourceClock struct {
	SourceID           string `json:"source_id"`
	RawAnchorNS        int64  `json:"raw_anchor_ns"`
	ContinuousAnchorNS int64  `json:"continuous_anchor_ns"`
}

// Offset returns continuous - raw anchor delta.
func (c SourceClock) Offset() (int64, error) {
	return CheckedAdd(c.ContinuousAnchorNS, -c.RawAnchorNS)
}

// EventsFromBlocks converts parsed EBML blocks to unified events.
func EventsFromBlocks(blocks []*Block, kindForTrack func(uint32) EventKind) ([]Event, error) {
	out := make([]Event, 0, len(blocks))
	for _, b := range blocks {
		kind := KindCue
		if kindForTrack != nil {
			kind = kindForTrack(b.TrackID)
		}
		end, err := CheckedAdd(b.PTSNS, b.DurationNS)
		if err != nil {
			return nil, NewError(StageTimeline, b.ElementOffset, b.Path, err).WithTrack(b.TrackID)
		}
		out = append(out, Event{
			TrackID:        b.TrackID,
			Sequence:       b.Sequence,
			GlobalSequence: b.GlobalSequence,
			Kind:           kind,
			PTSNS:          b.PTSNS,
			DurationNS:     b.DurationNS,
			EndNS:          end,
			SourceID:       b.SourceID,
			Offset:         b.ElementOffset,
		})
	}
	return out, nil
}

// NormalizeSourceClocks applies anchor offsets to events from renamed backup
// sources. Events with an empty SourceID or no matching clock are unchanged.
func NormalizeSourceClocks(events []Event, clocks []SourceClock) ([]Event, error) {
	bySource := make(map[string]SourceClock, len(clocks))
	for _, c := range clocks {
		bySource[c.SourceID] = c
	}
	out := make([]Event, len(events))
	copy(out, events)
	for i := range out {
		c, ok := bySource[out[i].SourceID]
		if !ok {
			continue
		}
		delta, err := c.Offset()
		if err != nil {
			return nil, NewError(StageTimeline, out[i].Offset, "", err).WithTrack(out[i].TrackID)
		}
		pts, err := CheckedAdd(out[i].PTSNS, delta)
		if err != nil {
			return nil, NewError(StageTimeline, out[i].Offset, "", err).WithTrack(out[i].TrackID)
		}
		out[i].PTSNS = pts
		if out[i].DurationNS > 0 {
			end, err := CheckedAdd(pts, out[i].DurationNS)
			if err != nil {
				return nil, NewError(StageTimeline, out[i].Offset, "", err).WithTrack(out[i].TrackID)
			}
			out[i].EndNS = end
		}
	}
	return out, nil
}

// SortEvents orders events on the continuous timeline. Equal timestamps are
// ordered by track number and then the per-track block/packet sequence.
func SortEvents(events []Event) []Event {
	out := append([]Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.PTSNS != b.PTSNS {
			return a.PTSNS < b.PTSNS
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if a.Sequence != b.Sequence {
			return a.Sequence < b.Sequence
		}
		return a.GlobalSequence < b.GlobalSequence
	})
	return out
}

// ValidateMonotonic checks that a sorted event list has non-decreasing PTS.
func ValidateMonotonic(events []Event) error {
	for i := 1; i < len(events); i++ {
		if events[i].PTSNS < events[i-1].PTSNS {
			return NewError(StageTimeline, events[i].Offset, "", ErrUnsortedEvents).
				WithTrack(events[i].TrackID)
		}
	}
	return nil
}

// ByKind returns events of one kind.
func ByKind(events []Event, kind EventKind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}
