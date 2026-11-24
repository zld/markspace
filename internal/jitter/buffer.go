// Package jitter implements a bounded reordering buffer.
//
// Frames arrive with extended sequence numbers that may be out of order. The
// Buffer holds at most Window sequence positions: packets that land inside the
// window participate in reordering, while a packet older than the window's
// release point is a late packet and produces a locatable CatLate error.
// Duplicates are reported but never released twice, so they cannot be timed
// twice. Missing positions are emitted as gaps instead of fabricated audio.
//
// Push is self-draining: whenever the window fills it releases the positions
// that can no longer change into a bounded output queue, so memory stays
// bounded regardless of stream length. Release consumes that queue.
package jitter

import (
	"fmt"

	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/rtp"
)

// Config configures the jitter Buffer.
type Config struct {
	// Window is the maximum number of sequence positions held for reordering.
	Window int
	// Node is stamped onto errors emitted by this buffer.
	Node string
}

// Event is one output step of the buffer: either a frame or a recorded gap.
type Event struct {
	// Frame is non-nil for an in-order release; nil for a gap slot.
	Frame *rtp.Frame
	// Gap is non-nil when this slot is a missing sequence position.
	Gap *Gap
}

// Gap records a missing extended sequence position.
type Gap struct {
	// Seq is the missing extended sequence number.
	Seq uint64
	// Node is the component that detected the gap.
	Node string
}

// Buffer is a fixed-capacity reordering buffer keyed by extended sequence.
type Buffer struct {
	cfg      Config
	slots    map[uint64]*rtp.Frame
	seen     map[uint64]struct{} // every accepted sequence, including released
	ready    uint64              // next expected sequence position
	high     uint64              // highest sequence position ever accepted
	started  bool
	draining bool
	out      []Event // bounded released events awaiting Release
}

// New validates the config and builds a Buffer.
func New(cfg Config) (*Buffer, error) {
	if cfg.Window <= 0 {
		return nil, fmt.Errorf("jitter: window must be positive, got %d", cfg.Window)
	}
	return &Buffer{
		cfg:   cfg,
		slots: make(map[uint64]*rtp.Frame),
		seen:  make(map[uint64]struct{}),
	}, nil
}

// Push adds a frame. It returns a CatDuplicate error for a repeated sequence
// (the caller may treat it as informational) and a CatLate error for a frame
// whose position was already released.
func (b *Buffer) Push(f *rtp.Frame) error {
	if !b.started {
		b.started = true
		b.ready = f.ExtSeq
		b.high = f.ExtSeq
	}
	if _, known := b.seen[f.ExtSeq]; known {
		return mpierr.New(mpierr.CatDuplicate, f.ExtSeq, b.cfg.Node,
			"duplicate sequence number dropped").AtOffset(f.StreamOffset)
	}
	if f.ExtSeq < b.ready {
		return mpierr.New(mpierr.CatLate, f.ExtSeq, b.cfg.Node,
			fmt.Sprintf("packet arrived after release point %d", b.ready)).
			AtOffset(f.StreamOffset)
	}
	b.seen[f.ExtSeq] = struct{}{}
	b.slots[f.ExtSeq] = f
	if f.ExtSeq > b.high {
		b.high = f.ExtSeq
	}
	b.advance()
	return nil
}

// advance moves every position that can no longer change into the output
// queue. While streaming, a position at the release point is emitted as a gap
// only once the window behind it is exhausted; at Drain, all remaining
// positions up to the highest accepted one are flushed.
func (b *Buffer) advance() {
	for b.ready <= b.high {
		if f, ok := b.slots[b.ready]; ok {
			delete(b.slots, b.ready)
			b.out = append(b.out, Event{Frame: f})
			b.ready++
			continue
		}
		// Missing at the release point.
		if !b.draining && b.high-b.ready < uint64(b.cfg.Window) {
			return // still inside the reorder window; wait
		}
		// Window exhausted (or draining): record the gap and move on.
		b.out = append(b.out, Event{Gap: &Gap{Seq: b.ready, Node: b.cfg.Node}})
		b.ready++
	}
}

// Drain marks the stream as finished so Release can flush buffered positions.
func (b *Buffer) Drain() {
	b.draining = true
	b.advance()
}

// Release returns the next available event and reports whether one existed.
func (b *Buffer) Release() (Event, bool) {
	if len(b.out) == 0 {
		return Event{}, false
	}
	ev := b.out[0]
	b.out = b.out[1:]
	return ev, true
}

// Ready returns the next sequence position the buffer will release.
func (b *Buffer) Ready() uint64 { return b.ready }

// Buffered returns the number of frames currently held in the reorder slots.
func (b *Buffer) Buffered() int { return len(b.slots) }
