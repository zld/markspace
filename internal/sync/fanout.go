package sync

import (
	"sort"

	"mediasync/internal/model"
	"mediasync/internal/rtp"
)

// Lane is one representation's independent processing state: its own jitter
// buffer and recovery state, isolated from every other bitrate layer.
type Lane struct {
	Representation string
	Kind           model.AssetKind
	Priority       model.Priority
	Buffer         *rtp.JitterBuffer
	Recoverer      *rtp.Recoverer
	Segments       []model.SegmentDescriptor // recovered, pre-clock segments
	Active         bool                      // false once budget logic suspends the lane
}

// Fanout maintains the independent per-representation lanes.
type Fanout struct {
	lanes map[string]*Lane
	order []string // deterministic insertion order
}

// NewFanout creates an empty fanout.
func NewFanout() *Fanout {
	return &Fanout{lanes: map[string]*Lane{}}
}

// AddLane registers a representation with its independent buffer/recoverer.
func (f *Fanout) AddLane(rep string, kind model.AssetKind, pri model.Priority,
	buffer *rtp.JitterBuffer, recoverer *rtp.Recoverer) *Lane {
	lane := &Lane{
		Representation: rep, Kind: kind, Priority: pri,
		Buffer: buffer, Recoverer: recoverer, Active: true,
	}
	f.lanes[rep] = lane
	f.order = append(f.order, rep)
	return lane
}

// Lane fetches a representation's lane.
func (f *Fanout) Lane(rep string) (*Lane, bool) {
	l, ok := f.lanes[rep]
	return l, ok
}

// Lanes returns all lanes in deterministic registration order.
func (f *Fanout) Lanes() []*Lane {
	out := make([]*Lane, 0, len(f.order))
	for _, name := range f.order {
		out = append(out, f.lanes[name])
	}
	return out
}

// Suspend deactivates a lane, recording that its representation stopped
// consuming budget. Core-priority lanes cannot be suspended: the function
// returns false and leaves them active so dialogue and subtitles stay whole.
func (f *Fanout) Suspend(rep string) bool {
	lane, ok := f.lanes[rep]
	if !ok || lane.Priority == model.PriorityCore {
		return false
	}
	lane.Active = false
	return true
}

// Dispatch places a descriptor into its representation lane. Unknown
// representations are ignored; suspended lanes drop new work rather than
// resurrecting themselves.
func (f *Fanout) Dispatch(s model.SegmentDescriptor) bool {
	lane, ok := f.lanes[s.Representation]
	if !ok || !lane.Active {
		return false
	}
	lane.Segments = append(lane.Segments, s)
	return true
}

// MergeInto fans every active lane into the shared clock and returns the
// combined, clock-aligned segment stream. Lanes are processed in registration
// order so the resulting cuts and hashes are deterministic.
func (f *Fanout) MergeInto(clock *Clock) []model.SegmentDescriptor {
	var merged []model.SegmentDescriptor
	for _, name := range f.order {
		lane := f.lanes[name]
		if !lane.Active {
			continue
		}
		merged = append(merged, clock.FanIn(lane.Segments)...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].PeriodID != merged[j].PeriodID {
			return merged[i].PeriodID < merged[j].PeriodID
		}
		if merged[i].StartNS != merged[j].StartNS {
			return merged[i].StartNS < merged[j].StartNS
		}
		if merged[i].Representation != merged[j].Representation {
			return merged[i].Representation < merged[j].Representation
		}
		return merged[i].Index < merged[j].Index
	})
	return merged
}
