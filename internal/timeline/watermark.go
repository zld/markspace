// Package timeline turns an unordered stream of timestamped events into
// sealed, ordered segments. Sealing is driven by an event-time watermark;
// events inside the reorder window are sorted, events behind the watermark
// force a deterministic recomputation of the affected sealed interval.
package timeline

import (
	"sort"

	"mediapipeline/internal/media"
)

// Event is a single timestamped observation. Time is the media (event) time;
// Track and Seq give a total order for events that share a timestamp, so the
// sealed output never depends on arrival order.
type Event struct {
	Time    media.Nano
	Track   uint32
	Seq     uint64
	Payload string
}

// Less defines the canonical deterministic order.
func (e Event) Less(o Event) bool {
	if e.Time != o.Time {
		return e.Time < o.Time
	}
	if e.Track != o.Track {
		return e.Track < o.Track
	}
	if e.Seq != o.Seq {
		return e.Seq < o.Seq
	}
	return e.Payload < o.Payload
}

// Interval is a half-open media-time range [Start, End).
type Interval struct {
	Start media.Nano
	End   media.Nano
}

// Segment is one sealed interval with its events in canonical order.
type Segment struct {
	Index   int
	Start   media.Nano
	End     media.Nano
	Events  []Event
	Replays int // number of recomputations that touched this segment
}

// LateRecord documents one late event and the interval it forced to recompute.
type LateRecord struct {
	Event    Event
	Interval Interval
}

// Config bounds the timeline's memory and replay behaviour.
type Config struct {
	// Segment is the target segment duration; every segment boundary is a
	// multiple of it.
	Segment media.Nano
	// AllowedLateness is how far behind the newest observed event the
	// watermark may fall.
	AllowedLateness media.Nano
	// MaxReplayWindow is the oldest media time, relative to the watermark,
	// that a late event may still trigger. Older events are rejected loudly.
	MaxReplayWindow media.Nano
	// MaxEventsPerSegment caps the events held for one segment.
	MaxEventsPerSegment int
}

// Validate checks the configuration for impossible or unbounded values.
func (c Config) Validate() error {
	if c.Segment <= 0 {
		return media.NewError(media.InvalidInput, "segment duration must be positive, got %d", int64(c.Segment))
	}
	if c.AllowedLateness < 0 {
		return media.NewError(media.InvalidInput, "allowed lateness must be non-negative")
	}
	if c.MaxReplayWindow < c.AllowedLateness {
		return media.NewError(media.InvalidInput,
			"max replay window (%d ns) must be at least allowed lateness (%d ns)",
			int64(c.MaxReplayWindow), int64(c.AllowedLateness))
	}
	if c.MaxEventsPerSegment <= 0 {
		return media.NewError(media.InvalidInput, "max events per segment must be positive")
	}
	return nil
}

// Watermark is the highest media time that can be sealed: the newest observed
// event time minus the allowed lateness. It never moves backwards.
type Watermark struct {
	lateness media.Nano
	maxSeen  media.Nano
	value    media.Nano
	started  bool
	advances int
}

// NewWatermark builds a watermark that trails the newest event by lateness.
func NewWatermark(lateness media.Nano) (*Watermark, error) {
	if lateness < 0 {
		return nil, media.NewError(media.InvalidInput, "allowed lateness must be non-negative")
	}
	return &Watermark{lateness: lateness}, nil
}

// Observe records an event time and advances the watermark if possible. It
// reports whether the event arrived behind the previous watermark (late) and
// the resulting watermark value.
func (w *Watermark) Observe(t media.Nano) (late bool, value media.Nano) {
	late = w.started && t < w.value
	if !w.started || t > w.maxSeen {
		w.maxSeen = t
		w.started = true
	}
	next := w.maxSeen - w.lateness
	if !w.started || next < 0 {
		next = 0
	}
	if next > w.value {
		w.value = next
		w.advances++
	}
	return late, w.value
}

// Value returns the current watermark.
func (w *Watermark) Value() media.Nano { return w.value }

// MaxSeen returns the newest observed event time.
func (w *Watermark) MaxSeen() media.Nano { return w.maxSeen }

// Advances returns how many times the watermark moved forward.
func (w *Watermark) Advances() int { return w.advances }

// Timeline seals segments once the watermark passes their end.
type Timeline struct {
	cfg       Config
	water     *Watermark
	sealed    []Segment
	pending   map[int][]Event // segment index -> events not yet sealed
	index     map[eventKey]bool
	late      []LateRecord
	counts    map[media.Category]int
	finalized bool
	end       media.Nano // exclusive end of the final sealed range after Flush
}

type eventKey struct {
	track uint32
	seq   uint64
	time  media.Nano
}

// New builds a Timeline from a validated configuration.
func New(cfg Config) (*Timeline, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	w, err := NewWatermark(cfg.AllowedLateness)
	if err != nil {
		return nil, err
	}
	return &Timeline{
		cfg:     cfg,
		water:   w,
		pending: make(map[int][]Event),
		index:   make(map[eventKey]bool),
		counts:  make(map[media.Category]int),
	}, nil
}

func (tl *Timeline) segIndex(t media.Nano) int {
	if t < 0 {
		return -1
	}
	return int(t / tl.cfg.Segment)
}

func (tl *Timeline) segBounds(idx int) Interval {
	start := media.Nano(idx) * tl.cfg.Segment
	return Interval{Start: start, End: start + tl.cfg.Segment}
}

// Push adds an event. Duplicates are counted and ignored; events older than
// the configured replay window fail; otherwise the event either joins the
// reorder buffer or triggers a replay of its sealed segment.
func (tl *Timeline) Push(ev Event) error {
	if ev.Time < 0 {
		return media.NewError(media.InvalidInput, "negative event time %d", int64(ev.Time))
	}
	if tl.finalized {
		return media.NewError(media.InvalidInput, "timeline is already finalized")
	}
	key := eventKey{track: ev.Track, seq: ev.Seq, time: ev.Time}
	if tl.index[key] {
		tl.counts[media.DuplicatePacket]++
		return nil
	}
	idx := tl.segIndex(ev.Time)
	if idx >= len(tl.sealed) {
		// Still inside the reorder window: buffer and sort on seal.
		pending := tl.pending[idx]
		if len(pending) >= tl.cfg.MaxEventsPerSegment {
			tl.counts[media.ReplayLimitExceeded]++
			b := tl.segBounds(idx)
			return media.NewError(media.ReplayLimitExceeded,
				"segment %d already holds %d buffered events", idx, len(pending)).
				AtOffset(int64(b.Start))
		}
		pending = append(pending, ev)
		tl.pending[idx] = pending
		tl.index[key] = true
		tl.water.Observe(ev.Time)
		return tl.sealReady()
	}
	// The event targets an already sealed segment: recompute it in place.
	bounds := tl.segBounds(idx)
	wm := tl.water.Value()
	if tl.cfg.MaxReplayWindow > 0 && wm > ev.Time && wm-ev.Time > tl.cfg.MaxReplayWindow {
		tl.counts[media.ReplayLimitExceeded]++
		return media.NewError(media.ReplayLimitExceeded,
			"late event at %d ns is %d ns behind watermark %d (limit %d)",
			int64(ev.Time), int64(wm-ev.Time), int64(wm), int64(tl.cfg.MaxReplayWindow)).
			AtTrack(int32(ev.Track)).AtOffset(int64(ev.Time))
	}
	seg := &tl.sealed[idx]
	if len(seg.Events)+1 > tl.cfg.MaxEventsPerSegment {
		tl.counts[media.ReplayLimitExceeded]++
		return media.NewError(media.ReplayLimitExceeded,
			"replaying segment %d would exceed %d events",
			idx, tl.cfg.MaxEventsPerSegment).AtOffset(int64(bounds.Start))
	}
	seg.Events = append(seg.Events, ev)
	seg.Replays++
	sortEvents(seg.Events)
	tl.index[key] = true
	tl.counts[media.LateEvent]++
	tl.late = append(tl.late, LateRecord{Event: ev, Interval: bounds})
	return nil
}

// sealReady seals every buffered segment whose end is at or below the
// watermark, stopping at the first gap so sealed output never skips data.
func (tl *Timeline) sealReady() error {
	wm := tl.water.Value()
	for {
		idx := len(tl.sealed)
		bounds := tl.segBounds(idx)
		if bounds.End > wm {
			return nil
		}
		evs, ok := tl.pending[idx]
		if !ok {
			return nil
		}
		sortEvents(evs)
		tl.sealed = append(tl.sealed, Segment{
			Index:  idx,
			Start:  bounds.Start,
			End:    bounds.End,
			Events: evs,
		})
		delete(tl.pending, idx)
	}
}

// Flush seals everything buffered, regardless of the watermark. Interior
// intervals with no events are still emitted as empty segments so that
// boundaries stay aligned across representations; they are counted as gaps so
// the absence is visible rather than silent.
func (tl *Timeline) Flush() error {
	if tl.finalized {
		return nil
	}
	// Observe the newest buffered event so trailing events become sealable.
	var highest int = -1
	for idx, evs := range tl.pending {
		for _, ev := range evs {
			tl.water.Observe(ev.Time)
		}
		if idx > highest {
			highest = idx
		}
	}
	gaps := 0
	for idx := len(tl.sealed); idx <= highest; idx++ {
		b := tl.segBounds(idx)
		evs, ok := tl.pending[idx]
		if !ok {
			gaps++
			tl.sealed = append(tl.sealed, Segment{Index: idx, Start: b.Start, End: b.End})
			continue
		}
		sortEvents(evs)
		tl.sealed = append(tl.sealed, Segment{Index: idx, Start: b.Start, End: b.End, Events: evs})
		delete(tl.pending, idx)
	}
	tl.finalized = true
	if gaps > 0 {
		tl.counts[media.UnrecoverableGap] += gaps
	}
	var maxEnd media.Nano
	for _, s := range tl.sealed {
		if s.End > maxEnd {
			maxEnd = s.End
		}
	}
	tl.end = maxEnd
	return nil
}

// Segments returns the sealed segments in order. The slice is a copy of the
// slice header; event slices are shared and must be treated as read-only.
func (tl *Timeline) Segments() []Segment { return tl.sealed }

// LateRecords returns the late events seen so far.
func (tl *Timeline) LateRecords() []LateRecord { return tl.late }

// Counts returns a copy of the diagnostic counters.
func (tl *Timeline) Counts() map[media.Category]int {
	out := make(map[media.Category]int, len(tl.counts))
	for k, v := range tl.counts {
		out[k] = v
	}
	return out
}

// Watermark returns the current watermark value.
func (tl *Timeline) Watermark() media.Nano { return tl.water.Value() }

// SealedEnd returns the exclusive end of sealed data (valid after Flush).
func (tl *Timeline) SealedEnd() media.Nano { return tl.end }

// EventCount returns the number of stored events across sealed segments.
func (tl *Timeline) EventCount() int {
	n := 0
	for _, s := range tl.sealed {
		n += len(s.Events)
	}
	return n
}

// sortEvents orders events canonically.
func sortEvents(evs []Event) {
	sort.Slice(evs, func(i, j int) bool { return evs[i].Less(evs[j]) })
}
