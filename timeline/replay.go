package timeline

import (
	"mediacore"
)

// Checkpoint captures the engine state needed to deterministically recompute
// everything at or after StartNS. Events are retained in arrival-independent,
// time-sorted order, so replaying the same set yields identical segments.
type Checkpoint struct {
	WatermarkNS int64
	Events      []mediacore.Event
	SegmentIDs  map[int]int // segment bucket -> committed segment ID
	NextID      int
}

// Engine is the recomputable timeline. Late events that land behind the
// watermark trigger deterministic replay from the nearest checkpoint; only
// affected segments are rebuilt and re-hashed, unaffected hashes stay put.
type Engine struct {
	seg        *Segmenter
	hasher     *SegmentHasher
	wm         *Watermark
	commits    map[int]*Segment // segment bucket index -> committed segment
	pending    []mediacore.Event
	nextID     int
	idOverride map[int]int // replay: stable IDs for rebuilt buckets

	// Replays counts deterministic replay passes triggered by late data.
	Replays int
	// Recomputed counts segments rebuilt during replay.
	Recomputed int
	// LateReplays counts late events that caused at least one recomputation.
	LateReplays int
}

// NewEngine builds a timeline engine with the given segment duration.
func NewEngine(durationNS int64) *Engine {
	return &Engine{
		seg:        NewSegmenter(durationNS),
		hasher:     NewSegmentHasher(),
		wm:         NewWatermark(),
		commits:    make(map[int]*Segment),
		idOverride: make(map[int]int),
	}
}

// Watermark exposes the cross-track high-water mark.
func (e *Engine) Watermark() int64 { return e.wm.Merged() }

// Checkpoint snapshots the current engine state for later rollback.
func (e *Engine) Checkpoint() Checkpoint {
	n := len(e.pending) + e.totalCommittedEvents()
	snap := make([]mediacore.Event, 0, n)
	snap = append(snap, e.pending...)
	for _, seg := range e.sortedCommits() {
		snap = append(snap, seg.Events...)
	}
	ids := make(map[int]int, len(e.commits))
	for k, v := range e.commits {
		ids[k] = v.ID
	}
	return Checkpoint{WatermarkNS: e.wm.Merged(), Events: snap, SegmentIDs: ids, NextID: e.nextID}
}

func (e *Engine) totalCommittedEvents() int {
	n := 0
	for _, seg := range e.commits {
		n += len(seg.Events)
	}
	return n
}

// Ingest feeds one mapped event into the engine. A late event (event time in
// a segment bucket that was already committed) triggers deterministic replay;
// otherwise it is buffered for the next commit.
func (e *Engine) Ingest(ev mediacore.Event) error {
	if len(e.pending)+e.totalCommittedEvents() >= mediacore.MaxSegments*64 {
		return mediacore.NewError(mediacore.CatLimit, ev.Track, "timeline.ingest", ev.Time,
			"event count exceeds bounded timeline capacity")
	}
	e.wm.Observe(ev)
	idx := e.seg.SegmentIndex(ev.Time)
	if _, committed := e.commits[idx]; committed {
		return e.replayWith(ev)
	}
	e.pending = append(e.pending, ev)
	return nil
}

// Commit builds all segments fully below the watermark, hashes them and
// records recovery actions. It returns newly committed segments.
func (e *Engine) Commit(actions ...Action) []*Segment {
	built := e.seg.Build(e.pending, e.wm.Merged())
	committed := make([]*Segment, 0, len(built))
	for _, seg := range built {
		idx := e.seg.SegmentIndex(seg.StartNS)
		seg.Actions = append(seg.Actions, actions...)
		// Deterministic index offset: the segment's position in the ordered
		// sequence of committed buckets, so it is stable across replay and
		// identical between independent runs.
		seg.IndexOff = e.indexOffset(idx)
		e.finalize(seg)
		switch {
		case e.idOverride != nil:
			if id, ok := e.idOverride[idx]; ok {
				seg.ID = id // replay preserves stable segment IDs
			} else if old, ok := e.commits[idx]; ok {
				seg.ID = old.ID
			} else {
				seg.ID = e.nextID
				e.nextID++
			}
		default:
			if old, ok := e.commits[idx]; ok {
				seg.ID = old.ID
			} else {
				seg.ID = e.nextID
				e.nextID++
			}
		}
		e.commits[idx] = seg
		committed = append(committed, seg)
	}
	e.pending = e.retainUncommitted()
	return committed
}

// indexOffset returns the ordinal position of a bucket among all buckets
// committed so far, counting only buckets at or below the argument.
func (e *Engine) indexOffset(bucket int) int {
	off := 0
	for b := range e.commits {
		if b < bucket {
			off++
		}
	}
	return off
}

// replayWith rolls back deterministically from the nearest checkpoint: every
// committed segment at or after the late event's bucket is unfrozen, its
// events are returned to the pending set together with the late arrival and
// the affected buckets are rebuilt through the normal commit path. Buckets
// before the event's bucket are never touched, so their hashes stay
// byte-identical; rebuilt buckets keep their original segment IDs.
func (e *Engine) replayWith(ev mediacore.Event) error {
	idx := e.seg.SegmentIndex(ev.Time)
	e.Replays++
	e.LateReplays++

	// 1. Capture stable IDs of every bucket that will be rebuilt.
	override := make(map[int]int)
	for b, seg := range e.commits {
		if b >= idx {
			override[b] = seg.ID
		}
	}
	// 2. Unfreeze affected buckets and reclaim their events in time order.
	reclaimed := make([]mediacore.Event, 0)
	for b := range e.commits {
		if b < idx {
			continue
		}
		reclaimed = append(reclaimed, e.commits[b].Events...)
		delete(e.commits, b)
	}
	// 3. Rebuild the pending set: previously pending + reclaimed + late event.
	restored := make([]mediacore.Event, 0, len(e.pending)+len(reclaimed)+1)
	restored = append(restored, e.pending...)
	restored = append(restored, reclaimed...)
	restored = append(restored, ev)
	e.pending = restored

	// 4. Re-commit deterministically with the stable ID mapping in force.
	e.idOverride = override
	built := e.Commit()
	e.idOverride = map[int]int{}
	e.Recomputed += len(built)
	return nil
}

// finalize freezes a segment's canonical hash from its ordered content.
func (e *Engine) finalize(seg *Segment) {
	seg.Hash = e.hasher.Hash(seg)
}

// Segments returns all committed segments in time order.
func (e *Engine) Segments() []*Segment { return e.sortedCommits() }

func (e *Engine) sortedCommits() []*Segment {
	out := make([]*Segment, 0, len(e.commits))
	for _, seg := range e.commits {
		out = append(out, seg)
	}
	// Insertion-order independent ordering for deterministic hashing.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartNS < out[j-1].StartNS; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (e *Engine) retainUncommitted() []mediacore.Event {
	wm := e.wm.Merged()
	out := make([]mediacore.Event, 0)
	for _, ev := range e.pending {
		if ev.Time >= wm {
			out = append(out, ev)
		}
	}
	return out
}
