// Package merge produces the deterministic event sequence of the
// pipeline: a total event order and a k-way ordered merge, plus the
// resumable event digest committed into checkpoints and manifests.
package merge

import (
	"container/heap"
	"encoding/binary"
	"sort"
)

// Event is one committed pipeline event on the nanosecond timeline.
type Event struct {
	TimestampNs int64
	Track       uint64
	Index       uint64 // per-track sequence
	Kind        string // "audio" or "video"
	DurationNs  int64
	Checksum    uint32
}

// Less is the total deterministic event order: timestamp, then track
// number, then per-track index.
func Less(a, b Event) bool {
	if a.TimestampNs != b.TimestampNs {
		return a.TimestampNs < b.TimestampNs
	}
	if a.Track != b.Track {
		return a.Track < b.Track
	}
	return a.Index < b.Index
}

// Sort orders events deterministically in place.
func Sort(events []Event) {
	sort.SliceStable(events, func(i, j int) bool { return Less(events[i], events[j]) })
}

// Merge k-way merges sorted event slices into one sorted slice. Ties
// between identical events from different streams break by stream
// order, keeping the result deterministic.
func Merge(streams ...[]Event) []Event {
	h := &eventHeap{}
	for i, s := range streams {
		if len(s) > 0 {
			heap.Push(h, item{ev: s[0], stream: i})
		}
	}
	var out []Event
	for h.Len() > 0 {
		it := heap.Pop(h).(item)
		out = append(out, it.ev)
		if it.pos+1 < len(streams[it.stream]) {
			heap.Push(h, item{ev: streams[it.stream][it.pos+1], stream: it.stream, pos: it.pos + 1})
		}
	}
	return out
}

type item struct {
	ev     Event
	stream int
	pos    int
}

type eventHeap []item

func (h eventHeap) Len() int { return len(h) }

func (h eventHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if Less(a.ev, b.ev) {
		return true
	}
	if Less(b.ev, a.ev) {
		return false
	}
	return a.stream < b.stream
}

func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *eventHeap) Push(x any) { *h = append(*h, x.(item)) }

func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// FNV-1a parameters for the resumable event digest. The digest state
// is a plain uint64 so a checkpoint can resume it exactly.
const (
	fnvOffset = uint64(14695981039346656037)
	fnvPrime  = uint64(1099511628211)
)

// DigestSeed is the initial digest state.
func DigestSeed() uint64 { return fnvOffset }

// DigestEvent folds one event into the running digest.
func DigestEvent(h uint64, ev Event) uint64 {
	h = digestUint64(h, uint64(ev.TimestampNs))
	h = digestUint64(h, ev.Track)
	h = digestUint64(h, ev.Index)
	h = digestBytes(h, []byte(ev.Kind))
	h = digestUint64(h, uint64(ev.DurationNs))
	h = digestUint64(h, uint64(ev.Checksum))
	return h
}

func digestUint64(h, v uint64) uint64 {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return digestBytes(h, b[:])
}

func digestBytes(h uint64, bs []byte) uint64 {
	for _, b := range bs {
		h ^= uint64(b)
		h *= fnvPrime
	}
	return h
}
