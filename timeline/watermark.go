package timeline

import (
	"sort"

	"mediacore"
)

// Watermark tracks the highest event time that has been observed on each
// track and the merged watermark across all tracks. It only advances on real
// observed events; wall-clock time never moves it.
type Watermark struct {
	perTrack map[mediacore.TrackID]int64
	merged   int64
}

// NewWatermark creates an empty watermark.
func NewWatermark() *Watermark {
	return &Watermark{perTrack: make(map[mediacore.TrackID]int64)}
}

// Observe records one event. The per-track and merged watermarks move forward
// only; an out-of-order older event never lowers them.
func (w *Watermark) Observe(ev mediacore.Event) (advanced bool, previous int64) {
	previous = w.merged
	if cur, ok := w.perTrack[ev.Track]; !ok || ev.Time > cur {
		w.perTrack[ev.Track] = ev.Time
	}
	// Merged watermark is the minimum of per-track high-water marks, so a
	// lagging track holds back shared commitment correctly.
	m := int64(1 << 62)
	for _, t := range w.perTrack {
		if t < m {
			m = t
		}
	}
	w.merged = m
	return w.merged > previous, previous
}

// Merged returns the current cross-track commit watermark.
func (w *Watermark) Merged() int64 { return w.merged }

// Track returns one track's high-water mark.
func (w *Watermark) Track(id mediacore.TrackID) (int64, bool) {
	t, ok := w.perTrack[id]
	return t, ok
}

// Action describes a recovery or normalization decision taken while building
// a segment, retained so every decision is explainable in the audit trail.
type Action struct {
	Kind   string // "pass", "recovered_silence", "gap", "drift_clamped", ...
	Track  mediacore.TrackID
	Offset int64
	Detail string
}

// Segment is one committed, hashable slice of the timeline.
type Segment struct {
	ID        int
	StartNS   int64
	EndNS     int64
	Events    []mediacore.Event
	Actions   []Action
	InputFrom int64 // lowest receive time among contributing events
	InputTo   int64 // highest receive time among contributing events
	IndexOff  int   // index/container offset assigned after indexing
	Hash      []byte
}

// Segmenter partitions ordered events into fixed-duration segments on the
// integer time line. Segments never overlap and boundary events belong to the
// segment containing their start time.
type Segmenter struct {
	durationNS int64
	nextID     int
}

// NewSegmenter builds a segmenter with the given segment duration.
func NewSegmenter(durationNS int64) *Segmenter {
	return &Segmenter{durationNS: durationNS}
}

// SegmentIndex returns the segment bucket an event at timeNS belongs to.
func (s *Segmenter) SegmentIndex(timeNS int64) int {
	if timeNS < 0 {
		return int((timeNS - s.durationNS + 1) / s.durationNS)
	}
	return int(timeNS / s.durationNS)
}

// SegmentStart returns the inclusive start time of a segment bucket.
func (s *Segmenter) SegmentStart(idx int) int64 { return int64(idx) * s.durationNS }

// Build groups ordered events into complete segments up to the watermark.
// Events at or after watermarkNS stay uncommitted. Returned segments carry
// their input event receive-time range for replay scoping.
func (s *Segmenter) Build(events []mediacore.Event, watermarkNS int64) []*Segment {
	buckets := make(map[int]*Segment)
	order := make([]int, 0)
	for _, ev := range events {
		if ev.Time >= watermarkNS {
			continue
		}
		idx := s.SegmentIndex(ev.Time)
		seg, ok := buckets[idx]
		if !ok {
			seg = &Segment{ID: s.nextID + len(order), StartNS: s.SegmentStart(idx),
				EndNS: s.SegmentStart(idx) + s.durationNS, InputFrom: ev.Recv, InputTo: ev.Recv}
			buckets[idx] = seg
			order = append(order, idx)
		}
		seg.Events = append(seg.Events, ev)
		if ev.Recv < seg.InputFrom {
			seg.InputFrom = ev.Recv
		}
		if ev.Recv > seg.InputTo {
			seg.InputTo = ev.Recv
		}
	}
	sort.Ints(order)
	out := make([]*Segment, 0, len(order))
	for _, idx := range order {
		out = append(out, buckets[idx])
	}
	return out
}
