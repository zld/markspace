// Package core runs the processing pipeline: unified timeline assembly,
// keyframe-aligned splitting, silence detection and fingerprinting.
package core

import (
	"fmt"

	"mediad/internal/model"
)

// EventKind classifies what happened when a frame was admitted to the
// unified timeline. Transport anomalies stay distinguishable here.
type EventKind string

const (
	EventRegular   EventKind = "regular"
	EventDuplicate EventKind = "duplicate"
	EventFallback  EventKind = "fallback"
	EventWrap      EventKind = "wrap"
	EventDiscont   EventKind = "discontinuity"
)

// TimelineEvent records one anomaly for the provenance log.
type TimelineEvent struct {
	Kind   EventKind      `json:"kind"`
	Rep    string         `json:"representation"`
	Frame  int            `json:"frame"`
	PTS    model.PlayTime `json:"pts_ns"`
	Detail string         `json:"detail,omitempty"`
}

// TimelineReport is the per-representation result of assembly. Frames carry
// absolute PTS on the unified nanosecond timeline.
type TimelineReport struct {
	RepID        string           `json:"rep_id"`
	Kind         model.TrackKind  `json:"kind"`
	Frames       []model.Frame    `json:"frames"`
	Events       []TimelineEvent  `json:"events"`
	Monotonic    bool             `json:"monotonic"`
	BlockedMerge bool             `json:"blocked_merge"`
	Track        *model.MediaTrack `json:"-"`
}

// SegmentInput pairs one parsed track with its manifest segment anchor.
type SegmentInput struct {
	Track       *model.MediaTrack
	Anchor      model.PlayTime // segment start on the unified timeline
	Discontinuity bool
}

// BuildTimeline concatenates parsed segments onto one monotonic playout
// timeline. Frame PTS is re-based by adding the segment anchor, so the
// discontinuity handling in the manifest parser decides where each segment
// starts and this function only enforces strict monotonicity.
func BuildTimeline(repID string, kind model.TrackKind, blocked bool, segs []SegmentInput) (*TimelineReport, error) {
	rep := &TimelineReport{RepID: repID, Kind: kind, BlockedMerge: blocked, Monotonic: true}
	var lastPTS model.PlayTime
	haveLast := false
	lastEntry := -1
	frameNo := 0
	for si, in := range segs {
		if in.Track == nil {
			return nil, model.NewError(model.KindInternal,
				model.SourceLoc{Representation: repID, Entry: si, Frame: -1},
				"nil track in segment input")
		}
		if in.Discontinuity {
			rep.Events = append(rep.Events, TimelineEvent{
				Kind: EventDiscont, Rep: repID, Frame: frameNo, PTS: in.Anchor,
				Detail: fmt.Sprintf("segment %d starts after discontinuity", si),
			})
		}
		for j, f := range in.Track.Frames {
			abs, err := model.Add(in.Anchor, f.PTS)
			if err != nil {
				return nil, model.WrapError(model.KindFieldOutOfRange,
					model.SourceLoc{Representation: repID, Entry: si, Frame: j}, err)
			}
			if haveLast && abs < lastPTS {
				return nil, model.NewError(model.KindTimestampFallback,
					model.SourceLoc{Representation: repID, Entry: si, Frame: frameNo},
					fmt.Sprintf("absolute pts %d precedes previous pts %d", abs, lastPTS))
			}
			// Equal PTS is a duplicate only within the same source segment.
			// Across a segment boundary the same timestamp is the legitimate
			// boundary frame of the next segment (often a fresh keyframe).
			if haveLast && abs == lastPTS && si == lastEntry {
				rep.Events = append(rep.Events, TimelineEvent{
					Kind: EventDuplicate, Rep: repID, Frame: frameNo, PTS: abs,
					Detail: "equal absolute presentation timestamp",
				})
				frameNo++
				continue
			}
			f.PTS = abs
			rep.Frames = append(rep.Frames, f)
			lastPTS = abs
			lastEntry = si
			haveLast = true
			frameNo++
		}
	}
	if len(rep.Frames) == 0 {
		return nil, model.NewError(model.KindTruncatedFrame,
			model.SourceLoc{Representation: repID, Entry: -1, Frame: -1},
			"timeline produced no frames")
	}
	return rep, nil
}

// MergeBlocked reports whether a representation is forbidden from
// cross-representation deduplication due to manifest misalignment.
func MergeBlocked(m *model.Manifest, repID string) bool {
	for _, ma := range m.Misaligned {
		if ma.RepA == repID || ma.RepB == repID {
			return true
		}
	}
	return false
}
