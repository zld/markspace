// Package buffer implements a bounded jitter buffer: out-of-order reordering,
// duplicate suppression, bounded waiting for in-window gaps and explicit
// over-window rejection. The buffer can never grow beyond its capacity.
package buffer

import (
	"fmt"
	"sort"

	"mediaqc/internal/model"
)

// Stats summarizes buffer behaviour for the evidence report.
type Stats struct {
	Received          int
	Duplicates        int
	Reordered         int
	RejectedOverWindow int
	Flushed           int
	// UnrecoveredGaps are gap ranges still open when the stream terminates.
	UnrecoveredGaps []model.Gap
}

// Buffer holds at most windowSize sequence slots per track.
type Buffer struct {
	materialID string
	trackID    string
	windowSize int

	pending map[uint64]model.Event
	// nextSeq is the next sequence that may be emitted in order.
	nextSeq uint64
	started bool
	stats   Stats
}

// New builds a jitter buffer for one track.
func New(materialID, trackID string, windowSize int) (*Buffer, error) {
	if windowSize <= 0 {
		return nil, model.NewError(model.CodeConfig, model.StageBuffer, materialID, trackID,
			"jitter window must be positive")
	}
	return &Buffer{
		materialID: materialID,
		trackID:    trackID,
		windowSize: windowSize,
		pending:   make(map[uint64]model.Event),
	}, nil
}

// Push inserts one event and returns any events now releasable in strict
// sequence order. Duplicate sequences are counted and dropped; sequences more
// than windowSize ahead of the current release head are rejected.
func (b *Buffer) Push(ev model.Event) ([]model.Event, error) {
	b.stats.Received++
	if !b.started {
		b.started = true
		b.nextSeq = ev.Sequence
	}
	// A sequence behind the release head is a late duplicate.
	if ev.Sequence < b.nextSeq {
		b.stats.Duplicates++
		return nil, model.NewError(model.CodeDuplicateSeq, model.StageBuffer, b.materialID, b.trackID,
			fmt.Sprintf("late/duplicate sequence %d < head %d", ev.Sequence, b.nextSeq))
	}
	if _, exists := b.pending[ev.Sequence]; exists {
		b.stats.Duplicates++
		return nil, model.NewError(model.CodeDuplicateSeq, model.StageBuffer, b.materialID, b.trackID,
			fmt.Sprintf("duplicate sequence %d already buffered", ev.Sequence))
	}
	// Bound the span of buffered sequences.
	if ev.Sequence >= b.nextSeq && ev.Sequence-b.nextSeq >= uint64(b.windowSize) {
		b.stats.RejectedOverWindow++
		return nil, model.NewError(model.CodeOutOfWindow, model.StageBuffer, b.materialID, b.trackID,
			fmt.Sprintf("sequence %d exceeds tolerance window %d from head %d",
				ev.Sequence, b.windowSize, b.nextSeq))
	}
	// Reordering means a non-head sequence had to be parked.
	if ev.Sequence > b.nextSeq {
		b.stats.Reordered++
	}
	b.pending[ev.Sequence] = ev
	return b.drain(), nil
}

// drain emits every contiguous sequence starting at nextSeq.
func (b *Buffer) drain() []model.Event {
	var out []model.Event
	for {
		ev, ok := b.pending[b.nextSeq]
		if !ok {
			break
		}
		out = append(out, ev)
		delete(b.pending, b.nextSeq)
		b.nextSeq++
	}
	b.stats.Flushed += len(out)
	return out
}

// Flush terminates the stream: remaining buffered events are emitted in order
// and any holes are recorded as unrecovered gaps. The buffer is reset for a
// possible reuse.
func (b *Buffer) Flush() ([]model.Event, Stats) {
	seqs := make([]uint64, 0, len(b.pending))
	for s := range b.pending {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	var out []model.Event
	var gaps []model.Gap
	cursor := b.nextSeq
	for _, s := range seqs {
		if s > cursor {
			gaps = append(gaps, model.Gap{
				TrackID: b.trackID, FirstSeq: cursor, LastSeq: s - 1, DeclaredBy: model.StageBuffer,
			})
		}
		out = append(out, b.pending[s])
		cursor = s + 1
	}
	b.stats.Flushed += len(out)
	b.stats.UnrecoveredGaps = gaps
	stats := b.stats

	// reset for reuse
	b.pending = make(map[uint64]model.Event)
	b.started = false
	b.stats = Stats{}
	return out, stats
}

// PendingCount reports how many sequences are currently parked.
func (b *Buffer) PendingCount() int { return len(b.pending) }

// Stats returns a snapshot of running counters.
func (b *Buffer) Stats() Stats {
	s := b.stats
	s.UnrecoveredGaps = append([]model.Gap(nil), b.stats.UnrecoveredGaps...)
	return s
}
