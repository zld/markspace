// Package buffer holds media events in a jitter buffer ordered by the
// unified event time, releasing them once the shared watermark has moved
// a configurable depth past their presentation time.
package buffer

import (
	"container/heap"

	"mediad/internal/model"
)

// Jitter orders events by (EventMs, Seq) and emits those whose
// presentation time lies at least depthMs behind the watermark.
type Jitter struct {
	depthMs int64
	pq      eventHeap
}

// NewJitter creates a buffer with the given emit depth in milliseconds.
func NewJitter(depthMs int64) *Jitter {
	return &Jitter{depthMs: depthMs}
}

// Push inserts an event. Duplicate extended sequence numbers are
// rejected by the caller, so the buffer stores each sample once.
func (j *Jitter) Push(ev *model.Event) {
	heap.Push(&j.pq, ev)
}

// PopReady returns all events releasable at the given watermark, in
// deterministic (EventMs, Seq) order.
func (j *Jitter) PopReady(watermarkMs int64) []*model.Event {
	var out []*model.Event
	for len(j.pq) > 0 && j.pq[0].EventMs+j.depthMs <= watermarkMs {
		out = append(out, heap.Pop(&j.pq).(*model.Event))
	}
	return out
}

// Flush drains the buffer in order, regardless of the watermark.
func (j *Jitter) Flush() []*model.Event {
	out := make([]*model.Event, 0, len(j.pq))
	for len(j.pq) > 0 {
		out = append(out, heap.Pop(&j.pq).(*model.Event))
	}
	return out
}

// Len reports the number of buffered events.
func (j *Jitter) Len() int { return len(j.pq) }

type eventHeap []*model.Event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, k int) bool {
	if h[i].EventMs != h[k].EventMs {
		return h[i].EventMs < h[k].EventMs
	}
	return h[i].Seq < h[k].Seq
}
func (h eventHeap) Swap(i, k int) { h[i], h[k] = h[k], h[i] }

func (h *eventHeap) Push(x any) { *h = append(*h, x.(*model.Event)) }

func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	ev := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return ev
}
