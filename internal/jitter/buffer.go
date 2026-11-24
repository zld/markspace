// Package jitter implements a bounded reordering buffer.
//
// Frames arrive with extended sequence numbers that may be out of order. The
// Buffer holds at most Window sequence positions: packets that land inside the
// window participate in reordering, while a packet older than the window's
// release point is a late packet and produces a locatable CatLate error.
// Duplicates are reported but never released twice, so they cannot be timed
// twice. Missing positions are emitted as gaps instead of fabricated audio.
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
	ready    uint64 // next expected sequence position
	high     uint64 // highest sequence position ever accepted
	started  bool
	draining bool
}

// New validates the config and builds a Buffer.
func New(cfg Config) (*Buffer, error) {
	if cfg.Window <= 0 {
		return nil, fmt.Errorf("jitter: window must be positive, got %d", cfg.Window)
	}
	return &Buffer{cfg: cfg, slots: make(map[uint64]*rtp.Frame)}, nil
}

// Push adds a frame. It returns a CatDuplicate error for a repeated sequence
// (the caller may treat it as informational) and a CatLate error for a frame
// whose position was already released.
func (b *Buffer) Push(f *rtp.Frame) error {
	if !b.started {
		b.started = true
		b.ready = f.ExtSeq
	}
	if f.ExtSeq < b.ready {
		return mpierr.New(mpierr.CatLate, f.ExtSeq, b.cfg.Node,
			fmt.Sprintf("packet arrived after release point %d", b.ready)).
			AtOffset(f.StreamOffset)
	}
	if _, exists := b.slots[f.ExtSeq]; exists {
		return mpierr.New(mpierr.CatDuplicate, f.ExtSeq, b.cfg.Node,
			"duplicate sequence number dropped").AtOffset(f.StreamOffset)
	}
	if len(b.slots) >= b.cfg.Window {
		return mpierr.New(mpierr.CatLimit, f.ExtSeq, b.cfg.Node,
			fmt.Sprintf("jitter window of %d positions is full", b.cfg.Window)).
			AtOffset(f.StreamOffset)
	}
	b.slots[f.ExtSeq] = f
	if f.ExtSeq > b.high {
		b.high = f.ExtSeq
	}
	return nil
}

// Drain marks the stream as finished so Release can flush buffered positions.
func (b *Buffer) Drain() { b.draining = true }

// Release emits the next available event and reports whether one was produced.
// While draining, any missing position is emitted as a gap. Otherwise a gap is
// emitted only once the window is full of newer positions, meaning the missing
// packet can no longer arrive inside the window.
func (b *Buffer) Release() (Event, bool) {
	if !b.started || b.ready > b.high {
		return Event{}, false
	}
	if f, ok := b.slots[b.ready]; ok {
		delete(b.slots, b.ready)
		b.ready++
		return Event{Frame: f}, true
	}
	// Missing at the release point. Decide whether to wait for reordering.
	if !b.draining {
		if b.high-b.ready < uint64(b.cfg.Window) {
			return Event{}, false // window not yet exhausted; keep waiting
		}
	}
	seq := b.ready
	b.ready++
	return Event{Gap: &Gap{Seq: seq, Node: b.cfg.Node}}, true
}

// Ready returns the next sequence position the buffer will release.
func (b *Buffer) Ready() uint64 { return b.ready }

// Buffered returns the number of frames currently held.
func (b *Buffer) Buffered() int { return len(b.slots) }
