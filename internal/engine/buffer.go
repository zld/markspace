package engine

import (
	"sort"

	"mediasync/internal/buffer"
	"mediasync/internal/model"
)

// queued is one accepted packet awaiting release from a track's reorder
// buffer. The accepted entry is retained as-is so output derivation is
// identical to the pure-log path.
type queued struct {
	entry    buffer.Entry
	accepted accepted
}

// pendingCount returns how many packets are held across all tracks, used by
// bounded-memory reporting and tests.
func (e *Engine) pendingCount() int {
	n := 0
	for _, q := range e.queues {
		n += len(q)
	}
	return n
}

// enqueue puts an accepted packet into its track's reorder queue and returns
// the entries that became releasable in extended-sequence order. Packets the
// caller has already classified as late are accepted without buffering, since
// delivery order does not affect the derived result.
func (e *Engine) enqueue(a accepted, arrival model.Nanos) []accepted {
	if a.Late {
		return []accepted{a}
	}
	q := e.queues[a.TrackID]
	for _, item := range q {
		if item.entry.Ext == a.Ext {
			return nil
		}
	}
	if len(q) >= e.opts.MaxPackets {
		err := model.NewError(model.CodeBufferOverflow, model.Loc{TrackID: a.TrackID},
			"reorder queue for track %d holds %d packets, limit %d", a.TrackID, len(q), e.opts.MaxPackets)
		e.Errors.Add(err)
		e.dropped++
		return nil
	}
	q = append(q, queued{
		entry: buffer.Entry{
			Ext: a.Ext, TrackID: a.TrackID, Kind: a.Kind, Keyframe: a.Keyframe,
			MediaTime: a.Raw, Arrival: arrival, Size: a.Size,
		},
		accepted: a,
	})
	sort.SliceStable(q, func(i, j int) bool { return q[i].entry.Ext < q[j].entry.Ext })
	e.queues[a.TrackID] = q
	return e.drainQueue(a.TrackID)
}

// initialSeq is the first extended sequence a fresh per-track reorder queue
// expects. The generated captures number media packets from this boundary; a
// stream whose first extended sequence lies ahead of it abandons the never-seen
// prefix through the window-expiry stage instead of growing without bound.
const initialSeq int64 = 1

// nextSeq returns the sequence immediately after the highest released one.
// A track with no release yet starts at initialSeq.
func (e *Engine) nextSeq(trackID uint32) int64 {
	if last, ok := e.releasedAt[trackID]; ok {
		return last + 1
	}
	return initialSeq
}

// drainQueue releases packets that can no longer be reordered.
//
// It first emits the contiguous prefix beginning at the next expected
// sequence: filling a hole releases the whole connected run. When a hole stays
// open and the highest sequence has advanced more than ReorderPackets beyond
// the missing sequence, the gap is treated as loss and the aged contiguous run
// is forced out. The single newest entry is retained as an anchor for one more
// step, so the packet that triggers expiry is never released in the same batch.
// Entries behind the release boundary are emitted immediately rather than
// held. Release order is ascending extended sequence order.
func (e *Engine) drainQueue(trackID uint32) []accepted {
	q := e.queues[trackID]
	if len(q) == 0 {
		return nil
	}
	next := e.nextSeq(trackID)
	cursor := next - 1
	var out []accepted

	// Emit any entry already at or behind the boundary. Extended sequences are
	// unique, so this only catches a very late or out-of-contract arrival; it
	// must never block the queue or move the release boundary backwards.
	i := 0
	for i < len(q) && q[i].entry.Ext <= cursor {
		out = append(out, q[i].accepted)
		i++
	}
	// Stage 1: release the contiguous in-order prefix.
	for i < len(q) && q[i].entry.Ext == cursor+1 {
		out = append(out, q[i].accepted)
		cursor = q[i].entry.Ext
		i++
	}

	if i < len(q) {
		// q[i].Ext > cursor+1: an unfilled hole at cursor+1.
		missing := cursor + 1
		high := q[len(q)-1].entry.Ext
		if high-missing > int64(e.opts.ReorderPackets) {
			// Find the maximal contiguous run beginning at q[i].
			j := i
			for j+1 < len(q) && q[j+1].entry.Ext == q[j].entry.Ext+1 {
				j++
			}
			// Never emit the newest entry in the triggering batch: retain it as
			// the anchor. lastEmit is the last run index allowed out.
			lastEmit := j
			if j == len(q)-1 {
				lastEmit = j - 1
			}
			if lastEmit >= i {
				for k := i; k <= lastEmit; k++ {
					out = append(out, q[k].accepted)
					cursor = q[k].entry.Ext
				}
				i = lastEmit + 1
			} else {
				// The run is exactly the retained newest anchor: abandon the
				// preceding hole and align the boundary just before it.
				cursor = q[i].entry.Ext - 1
			}
		}
	}

	e.releasedAt[trackID] = cursor
	if i > 0 {
		e.queues[trackID] = q[i:]
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Ext < out[b].Ext })
	return out
}

// flushQueues releases every held packet at end of stream.
func (e *Engine) flushQueues() []accepted {
	var out []accepted
	ids := make([]uint32, 0, len(e.queues))
	for id := range e.queues {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		q := e.queues[id]
		for _, item := range q {
			out = append(out, item.accepted)
		}
		delete(e.queues, id)
	}
	return out
}
