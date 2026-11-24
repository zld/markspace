// Package reorder deduplicates and reorders RTP packets inside a configurable
// window and turns unrecoverable missing runs into explicit gaps. It never
// synthesizes audio data: gaps are reported as events for the caller to mark.
package reorder

import (
	"mediad/internal/clock"

	"github.com/pion/rtp"
)

// DefaultWindow is the default maximum tolerated reorder distance.
const DefaultWindow uint64 = 64

// Packet is one parsed RTP packet with its raw stream offset.
type Packet struct {
	Seq    uint16
	Offset int64
	RTP    *rtp.Packet
}

// EventKind distinguishes delivered packets from declared gaps.
type EventKind uint8

const (
	KindPacket EventKind = iota
	KindGap
)

// Event is one ordered output item. Gap events carry the missing seq range.
type Event struct {
	Kind   EventKind
	ExtSeq uint64
	Packet *Packet
	Gap    Gap
}

// Stats are the counters surfaced in the final delivery report.
type Stats struct {
	Received    uint64 // packets accepted into the window
	Duplicates  uint64 // exact seq seen more than once
	Reordered   uint64 // packets that arrived out of order within the window
	LateDropped uint64 // packets past the window whose seq was never delivered
}

// Window reorders packets by extended sequence number.
type Window struct {
	size      uint64
	unwrap    *clock.SeqUnwrapper
	loss      *LossTracker
	pending   map[uint64]*Packet
	delivered map[uint64]struct{}
	next      uint64
	started   bool
	stats     Stats
}

// NewWindow builds a window of the given size and burst threshold.
func NewWindow(size, burstThreshold uint64) *Window {
	if size == 0 {
		size = DefaultWindow
	}
	return &Window{
		size:      size,
		unwrap:    &clock.SeqUnwrapper{},
		loss:      NewLossTracker(burstThreshold),
		pending:   map[uint64]*Packet{},
		delivered: map[uint64]struct{}{},
	}
}

// Push inserts one arrival and returns any events that became deliverable
// (packets and, when the window forces a jump, gaps). Order is by ext seq.
func (w *Window) Push(p *Packet) []Event {
	ext := w.unwrap.Update(p.Seq)
	if !w.started {
		w.started = true
		w.next = ext // first observed packet anchors the frontier; no prior gaps
	}
	w.stats.Received++

	if ext < w.next {
		if _, seen := w.delivered[ext]; seen {
			w.stats.Duplicates++
		} else {
			w.stats.LateDropped++ // position already declared a gap
		}
		return nil
	}
	if _, dup := w.pending[ext]; dup {
		w.stats.Duplicates++
		return nil
	}

	var events []Event
	if ext != w.next {
		if ext-w.next <= w.size {
			w.stats.Reordered++
		}
		// Far-ahead arrivals expire everything more than one window behind.
		for ext-w.next > w.size {
			e := w.advance()
			if e.Kind == KindGap {
				e.Gap.Offset = p.Offset // locate the hole at the arrival that forced it
			}
			events = append(events, e)
		}
	}
	w.pending[ext] = p

	for {
		q, ok := w.pending[w.next]
		if !ok {
			break
		}
		delete(w.pending, w.next)
		w.delivered[w.next] = struct{}{}
		events = append(events, Event{Kind: KindPacket, ExtSeq: w.next, Packet: q})
		w.next++
	}
	w.evictSeen()
	return events
}

// advance moves the frontier one step, delivering a pending packet or
// recording an unrecoverable gap.
func (w *Window) advance() Event {
	if q, ok := w.pending[w.next]; ok {
		delete(w.pending, w.next)
		w.delivered[w.next] = struct{}{}
		e := Event{Kind: KindPacket, ExtSeq: w.next, Packet: q}
		w.next++
		return e
	}
	g := w.loss.Record(w.next, w.next, -1)
	e := Event{Kind: KindGap, ExtSeq: w.next, Gap: g}
	w.next++
	return e
}

// evictSeen bounds the delivered-seq set to one window of history.
func (w *Window) evictSeen() {
	floor := uint64(0)
	if w.next > w.size {
		floor = w.next - w.size
	}
	for k := range w.delivered {
		if k < floor {
			delete(w.delivered, k)
		}
	}
}

// Flush drains the window at end of stream: pending packets are delivered in
// seq order and remaining holes are declared gaps.
func (w *Window) Flush() []Event {
	var events []Event
	for len(w.pending) > 0 {
		events = append(events, w.advance())
	}
	return events
}

// Stats returns a snapshot of the window counters.
func (w *Window) Stats() Stats { return w.stats }

// Loss exposes the loss tracker for gap/burst reporting.
func (w *Window) Loss() *LossTracker { return w.loss }

// NextSeq returns the current frontier (next expected extended seq).
func (w *Window) NextSeq() uint64 { return w.next }
