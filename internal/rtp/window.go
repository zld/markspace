// Package rtp implements a bounded reorder window over RTP sequence numbers.
// It reorders packets that arrive late but within the window, counts exact
// duplicates, marks losses that exceed the window as unrecoverable, and never
// rewrites a range that has already been committed downstream.
package rtp

import (
	"context"
	"fmt"

	"mediapipe/internal/model"
)

// Packet is the subset of an RTP packet the window needs. Sequence and
// Timestamp come straight from the wire (16 and 32 bit, wrapping).
type Packet struct {
	Sequence  uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
}

// Delivered is a packet emitted in strict sequence order.
type Delivered struct {
	Sequence  uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
	Reordered bool // emitted from the buffer rather than on arrival
	Recovered bool // filled a slot that had already missed its turn
}

// Stats is the observable outcome of a window run.
//
// A gap is a sequence slot passed without its packet: either because a late
// packet supplied it after a later one had already been buffered (Recovered),
// or because the window slid past it, or the stream ended, without it
// (Unrecoverable). LossRecoveryRate is Recovered/(Recovered+Unrecoverable),
// so a run whose out-of-order arrivals all fall inside the window scores 1.
type Stats struct {
	Received      int // packets pushed, duplicates included
	Committed     int // packets emitted downstream
	InOrder       int // emitted immediately on arrival
	Reordered     int // arrivals ahead of the expected slot that were buffered
	Duplicates    int // re-offers of an already committed or buffered sequence
	Recovered     int // gaps filled by a late arrival within the window
	Unrecoverable int // gaps that passed beyond the window or remained at end
	Dropped       int // packets refused because the buffer was full
}

// Gaps returns the number of sequence slots that were ever missing.
func (s Stats) Gaps() int { return s.Recovered + s.Unrecoverable }

// LossRecoveryRate is the share of observed gaps repaired by the window, in
// [0,1]. It is 1 when no gap was ever observed.
func (s Stats) LossRecoveryRate() float64 {
	if s.Gaps() == 0 {
		return 1
	}
	return float64(s.Recovered) / float64(s.Gaps())
}

// Window reorders packets within a fixed span. It is not safe for concurrent
// use: the pipeline owns one goroutine per window.
type Window struct {
	size        int
	maxBuffered int
	next        uint16
	high        uint16
	started     bool
	buf         map[uint16]Packet
	missing     map[uint16]struct{}
	stats       Stats
}

// Config tunes the window.
type Config struct {
	// Size is how many sequence numbers ahead of the expected slot a packet
	// may be before the slots it skipped become unrecoverable.
	Size int
	// MaxBuffered bounds buffered packets independently of the span.
	MaxBuffered int
}

// DefaultConfig matches the demo's --window default for Size.
func DefaultConfig() Config { return Config{Size: 64, MaxBuffered: 8 * 1024} }

// New creates a window that will start at the first packet it sees.
func New(cfg Config) (*Window, error) {
	d := DefaultConfig()
	if cfg.Size <= 0 {
		cfg.Size = d.Size
	}
	if cfg.MaxBuffered <= 0 {
		cfg.MaxBuffered = d.MaxBuffered
	}
	if cfg.Size > 32767 {
		return nil, model.New(model.KindInvalid, model.StageRTP, "", "new",
			fmt.Errorf("window size %d exceeds half the 16-bit sequence space", cfg.Size))
	}
	return &Window{
		size:        cfg.Size,
		maxBuffered: cfg.MaxBuffered,
		buf:         make(map[uint16]Packet),
		missing:     make(map[uint16]struct{}),
	}, nil
}

// Stats returns a snapshot of the counters.
func (w *Window) Stats() Stats { return w.stats }

// ahead reports whether a is strictly after b in modular sequence order.
func ahead(a, b uint16) bool { return int16(a-b) > 0 }

func copyPacket(p Packet) Packet {
	c := p
	c.Payload = append([]byte(nil), p.Payload...)
	return c
}

// Push accepts one packet and returns every packet that became deliverable in
// strict sequence order. Payloads are copied so callers may reuse buffers.
func (w *Window) Push(p Packet) ([]Delivered, error) {
	w.stats.Received++
	if !w.started {
		w.started = true
		w.next = p.Sequence
		w.high = p.Sequence
	}

	if _, buffered := w.buf[p.Sequence]; buffered {
		w.stats.Duplicates++
		return nil, nil
	}
	if p.Sequence != w.next && !ahead(p.Sequence, w.next) {
		// Behind the expected slot: already committed downstream. Accepting
		// it would rewrite history, so it is only counted as a duplicate.
		w.stats.Duplicates++
		return nil, nil
	}

	if p.Sequence == w.next {
		recovered := w.takeMissing(p.Sequence)
		if !recovered {
			w.stats.InOrder++
		}
		out := []Delivered{w.emit(p, false, recovered)}
		w.next++
		return append(out, w.drain()...), nil
	}

	// The packet is ahead of the expected slot: every slot it skipped that
	// is not already buffered is now a known gap.
	w.noteGaps(p.Sequence)
	if span := int(p.Sequence - w.next); span > w.size {
		// Slots more than size behind this packet are beyond the recovery
		// window; slide forward marking them unrecoverable.
		out := w.skipTo(p.Sequence - uint16(w.size))
		if len(w.buf) >= w.maxBuffered {
			w.stats.Dropped++
			return out, model.New(model.KindUnrecoverable, model.StageRTP, "", "push",
				fmt.Errorf("reorder buffer full: %d packets buffered", len(w.buf))).
				WithOffset(int64(p.Sequence))
		}
		w.buf[p.Sequence] = copyPacket(p)
		w.stats.Reordered++
		w.advanceHigh(p.Sequence)
		return append(out, w.drain()...), nil
	}
	if len(w.buf) >= w.maxBuffered {
		w.stats.Dropped++
		return nil, model.New(model.KindUnrecoverable, model.StageRTP, "", "push",
			fmt.Errorf("reorder buffer full: %d packets buffered", len(w.buf))).
			WithOffset(int64(p.Sequence))
	}
	w.stats.Reordered++
	w.buf[p.Sequence] = copyPacket(p)
	w.advanceHigh(p.Sequence)
	return nil, nil
}

// noteGaps records slots between the expected next one and seq that have
// neither arrived nor been buffered. A slot recorded here is later either
// recovered (its packet arrives in time) or unrecoverable (the window slides
// past it or the stream ends).
func (w *Window) noteGaps(seq uint16) {
	for s := w.next; s != seq; s++ {
		if _, ok := w.buf[s]; ok {
			continue
		}
		w.missing[s] = struct{}{}
	}
}

// takeMissing removes seq from the gap set and reports whether it was there.
func (w *Window) takeMissing(seq uint16) bool {
	if _, ok := w.missing[seq]; !ok {
		return false
	}
	delete(w.missing, seq)
	return true
}

func (w *Window) advanceHigh(seq uint16) {
	if ahead(seq, w.high) {
		w.high = seq
	}
}

func (w *Window) emit(p Packet, reordered, recovered bool) Delivered {
	w.stats.Committed++
	if recovered {
		w.stats.Recovered++
	}
	return Delivered{
		Sequence:  p.Sequence,
		Timestamp: p.Timestamp,
		Marker:    p.Marker,
		Payload:   p.Payload,
		Reordered: reordered,
		Recovered: recovered,
	}
}

// drain emits every buffered packet that has become the expected next one.
func (w *Window) drain() []Delivered {
	var out []Delivered
	for {
		p, ok := w.buf[w.next]
		if !ok {
			return out
		}
		delete(w.buf, w.next)
		out = append(out, w.emit(p, true, w.takeMissing(w.next)))
		w.next++
	}
}

// skipTo advances next to target. Buffered packets on the way are emitted in
// order; slots with no packet are counted unrecoverable.
func (w *Window) skipTo(target uint16) []Delivered {
	var out []Delivered
	for w.next != target {
		if p, ok := w.buf[w.next]; ok {
			delete(w.buf, w.next)
			out = append(out, w.emit(p, true, w.takeMissing(w.next)))
		} else {
			if w.takeMissing(w.next) {
				w.stats.Unrecoverable++
			}
		}
		w.next++
	}
	return out
}

// Finalise ends the run. Buffered packets are still delivered in sequence
// order; holes below the high-water mark count as unrecoverable because the
// sender has stopped and nothing can fill them any more.
func (w *Window) Finalise() []Delivered {
	if !w.started {
		return nil
	}
	var out []Delivered
	for w.next != w.high+1 {
		if p, ok := w.buf[w.next]; ok {
			delete(w.buf, w.next)
			out = append(out, w.emit(p, true, w.takeMissing(w.next)))
		} else {
			if w.takeMissing(w.next) {
				w.stats.Unrecoverable++
			}
		}
		w.next++
	}
	return out
}

// Stream consumes packets from in and emits them on out until in closes or ctx
// is cancelled. The error returned is the single reason propagated onward.
func (w *Window) Stream(ctx context.Context, in <-chan Packet, out chan<- Delivered) error {
	for {
		select {
		case <-ctx.Done():
			return model.New(model.KindCancelled, model.StageRTP, "", "stream", ctx.Err())
		case p, ok := <-in:
			if !ok {
				if err := sendAll(ctx, out, w.Finalise()); err != nil {
					return err
				}
				return nil
			}
			got, err := w.Push(p)
			if err != nil {
				return err
			}
			if err := sendAll(ctx, out, got); err != nil {
				return err
			}
		}
	}
}

func sendAll(ctx context.Context, out chan<- Delivered, ps []Delivered) error {
	for _, d := range ps {
		select {
		case <-ctx.Done():
			return model.New(model.KindCancelled, model.StageRTP, "", "send", ctx.Err())
		case out <- d:
		}
	}
	return nil
}
