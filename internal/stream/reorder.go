// Package stream consumes a scan index and frame sequence, restores order,
// localizes damage and emits frames on the unified timebase.
package stream

import (
	"mediainspect/internal/media"
)

// ReorderStats counts how the reorder window treated the input.
type ReorderStats struct {
	Duplicates  int // frames seen before; counted, not emitted again
	LateDropped int // frames arriving behind the window; dropped and counted
	Lost        int // sequence numbers never delivered within the window
	Recovered   int // frames arrived behind the frontier, emitted in position
}

// Reorderer restores sequence order within a bounded window. A frame is
// emitted only once the frontier has advanced a full window past it, so
// memory use is O(window) and no frame is ever dropped silently.
type Reorderer struct {
	window  int
	next    int64 // extended sequence number to emit next
	anchor  int64 // first sequence ever seen; pre-anchor gaps are not loss
	maxExt  int64 // highest extended sequence seen (the frontier)
	started bool
	buf     map[int64]*media.Frame
	seen    map[int64]bool
	Stats   ReorderStats
}

func NewReorderer(window int) *Reorderer {
	if window < 1 {
		window = 1
	}
	return &Reorderer{
		window: window,
		buf:    make(map[int64]*media.Frame),
		seen:   make(map[int64]bool),
	}
}

// Push ingests one frame and returns any frames that became emittable in
// sequence order.
func (r *Reorderer) Push(f *media.Frame) []*media.Frame {
	if !r.started {
		r.next = int64(f.Seq) - int64(r.window) + 1
		r.anchor = int64(f.Seq)
		r.maxExt = int64(f.Seq)
		r.started = true
	}
	ext := media.ExtendSeq(f.Seq, r.maxExt)
	if r.seen[ext] {
		r.Stats.Duplicates++
		return nil
	}
	if ext < r.next {
		r.Stats.LateDropped++
		return nil
	}
	r.seen[ext] = true
	if ext < r.maxExt {
		r.Stats.Recovered++
	}
	if ext > r.maxExt {
		r.maxExt = ext
	}
	r.buf[ext] = f
	return r.drain()
}

// Flush emits everything still buffered, counting gaps as lost.
func (r *Reorderer) Flush() []*media.Frame {
	var out []*media.Frame
	for len(r.buf) > 0 {
		f, ok := r.buf[r.next]
		if !ok {
			if r.next >= r.anchor {
				r.Stats.Lost++
			}
			r.next++
			continue
		}
		out = append(out, f)
		delete(r.buf, r.next)
		r.next++
	}
	return out
}

// drain emits every frame that fell a full window behind the frontier.
func (r *Reorderer) drain() []*media.Frame {
	var out []*media.Frame
	for r.maxExt-r.next >= int64(r.window) {
		f, ok := r.buf[r.next]
		if !ok {
			if r.next >= r.anchor {
				r.Stats.Lost++
			}
			r.next++
			continue
		}
		out = append(out, f)
		delete(r.buf, r.next)
		r.next++
		// Evict seen entries far behind the window to bound memory.
		delete(r.seen, r.next-int64(r.window)-1)
	}
	return out
}
