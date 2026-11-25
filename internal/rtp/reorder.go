package rtp

import (
	"sort"

	"tracecheck/internal/model"
)

// Gap is a range of extended sequence numbers never observed. The span is
// estimated from the neighboring packets so the report can locate the
// damaged region; the kernel never synthesizes audio for missing numbers.
type Gap struct {
	FromExtended uint32     `json:"from_extended"`
	ToExtended   uint32     `json:"to_extended"`
	Count        uint32     `json:"count"`
	Span         model.Span `json:"span"`
}

// ReorderBuffer consumes packet records in arrival order, classifies them
// with SequenceTracker and emits contiguous packet ranges once the reorder
// window has passed. Missing numbers that the window cannot fill become
// gaps, so reordering and real loss are kept distinct.
type ReorderBuffer struct {
	tracker   *SequenceTracker
	window    uint16
	duplicates int
	reorders   int
	wraps      int
	pending   map[uint32]model.PacketRecord
	gaps      []Gap
	// highWater is the highest extended number whose prefix (every number
	// up to it) has been observed or declared missing.
	highWater uint32
	// lastReleased remembers the final packet released so far; gaps whose
	// left neighbor already left the window still get a real start time.
	lastReleased *model.PacketRecord
	started      bool
}

// NewReorderBuffer creates a buffer with the given maximum reorder distance.
func NewReorderBuffer(window uint16) *ReorderBuffer {
	return &ReorderBuffer{
		tracker: NewSequenceTracker(window),
		window:  window,
		pending: make(map[uint32]model.PacketRecord),
	}
}

// Push feeds one captured packet. It returns the tracker observation. The
// buffer itself is drained through Drain and Flush.
func (b *ReorderBuffer) Push(rec model.PacketRecord) Observation {
	obs := b.tracker.Push(rec.Sequence)
	if obs.Duplicate {
		b.duplicates++
		return obs
	}
	if obs.Reordered {
		b.reorders++
	}
	if obs.Wrapped {
		b.wraps++
	}
	rec.ExtendedSequence = obs.Extended
	b.pending[obs.Extended] = rec
	if !b.started {
		b.started = true
		b.highWater = obs.Extended
	}
	return obs
}

// Drain releases every contiguous range that can no longer be affected by
// late arrival: numbers more than window behind the current maximum. The
// boundary number itself stays buffered because a packet exactly window
// positions late is still in flight. Any holes inside the released prefix
// become gaps bounded by their neighbors.
func (b *ReorderBuffer) Drain() []model.PacketRecord {
	maxExt := b.tracker.MaxExtended()
	if !b.started || maxExt <= uint32(b.window)+1 {
		return nil
	}
	releaseUpTo := maxExt - uint32(b.window) - 1
	return b.releaseThrough(releaseUpTo)
}

// Flush ends the stream: all remaining holes are declared gaps and every
// buffered packet is released in extended-sequence order.
func (b *ReorderBuffer) Flush() []model.PacketRecord {
	if !b.started {
		return nil
	}
	return b.releaseThrough(b.tracker.MaxExtended())
}

func (b *ReorderBuffer) releaseThrough(limit uint32) []model.PacketRecord {
	var out []model.PacketRecord
	for b.highWater <= limit {
		ext := b.highWater
		if rec, ok := b.pending[ext]; ok {
			out = append(out, rec)
			delete(b.pending, ext)
			recCopy := rec
			b.lastReleased = &recCopy
			b.highWater = ext + 1
			continue
		}
		b.recordGap(ext, limit)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return extendedOf(out[i]) < extendedOf(out[j])
	})
	return out
}

// recordGap consumes the whole missing run starting at ext and records one
// gap spanning the observed packets on either side.
func (b *ReorderBuffer) recordGap(ext, limit uint32) {
	from := ext
	for ext <= limit {
		if _, ok := b.pending[ext]; ok {
			break
		}
		if ext == limit {
			ext++
			break
		}
		ext++
	}
	to := ext // first present number after the hole, or limit+1
	count := to - from
	span := b.estimateSpan(from, to)
	b.gaps = append(b.gaps, Gap{
		FromExtended: from,
		ToExtended:   to - 1,
		Count:        count,
		Span:         span,
	})
	b.highWater = to
}

// estimateSpan locates the gap on the tick clock using the nearest observed
// neighbors. The right neighbor is looked up first (it is still buffered at
// release time), then the left; a missing side collapses to the other one.
func (b *ReorderBuffer) estimateSpan(from, to uint32) model.Span {
	var start model.Tick
	okPrev := false
	if from > 0 {
		if rec, ok := b.nearestPresent(from-1, -1); ok {
			start, okPrev = rec.ArrivalTick, true
		} else if b.lastReleased != nil {
			start, okPrev = b.lastReleased.ArrivalTick, true
		}
	}
	next, okNext := b.nearestPresent(to, 1)
	switch {
	case okPrev && okNext:
		return model.NewSpan(start, next.ArrivalTick)
	case okPrev:
		return model.NewSpan(start, start)
	case okNext:
		return model.NewSpan(next.ArrivalTick, next.ArrivalTick)
	default:
		return model.Span{}
	}
}

func (b *ReorderBuffer) nearestPresent(ext uint32, dir int) (model.PacketRecord, bool) {
	maxExt := b.tracker.MaxExtended()
	for {
		if rec, ok := b.pending[ext]; ok {
			return rec, true
		}
		if dir < 0 {
			if ext == 0 {
				return model.PacketRecord{}, false
			}
			ext--
		} else {
			if ext >= maxExt {
				return model.PacketRecord{}, false
			}
			ext++
		}
	}
}

// Gaps returns the gaps detected so far.
func (b *ReorderBuffer) Gaps() []Gap { return b.gaps }

// Duplicates returns the number of duplicate packets observed.
func (b *ReorderBuffer) Duplicates() int { return b.duplicates }

// Reorders returns the count of packets that arrived out of order.
func (b *ReorderBuffer) Reorders() int { return b.reorders }

// Wraps returns the number of wrap events observed.
func (b *ReorderBuffer) Wraps() int { return b.wraps }

// Pending returns how many packets are still held inside the window.
func (b *ReorderBuffer) Pending() int { return len(b.pending) }

func extendedOf(rec model.PacketRecord) uint32 {
	return rec.ExtendedSequence
}
