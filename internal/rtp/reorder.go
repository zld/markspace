// Package rtp reassembles RTP packet order: it unwraps 16-bit sequence
// numbers across wraparound, reorders within a configurable wait window,
// counts duplicates once, and records gaps as loss once the window closes.
// It never fabricates media data for missing sequence numbers.
package rtp

import (
	"github.com/pion/rtp"

	"mediarebuild/internal/model"
)

// Stats summarises a reordering run.
type Stats struct {
	Received int `json:"received"` // total packets seen (incl. duplicates)
	Unique   int `json:"unique"`   // distinct extended sequence numbers delivered
	Dups     int `json:"dups"`     // packets dropped as duplicates
	Lost     int `json:"lost"`     // sequence numbers missing when the window closed
}

// Packet is a parsed RTP packet bound to its raw input offset.
type Packet struct {
	Offset  int64
	Parsed  *rtp.Packet
	ExtSeq  uint64 // wraparound-expanded sequence number
	Payload []byte
}

// Reorderer holds packets until their position is confirmed or the wait
// window closes. Window is measured in sequence numbers.
type Reorderer struct {
	Window   uint64
	stats    Stats
	init     bool
	highest  uint64            // highest extended seq received
	nextEmit uint64            // next extended seq to finalise
	pending  map[uint64]Packet // buffered, keyed by extended seq
	seen     map[uint64]bool   // every ext seq ever accepted (dup detect)
}

// NewReorderer creates a Reorderer with the given wait window (>= 1).
func NewReorderer(window uint64) *Reorderer {
	if window < 1 {
		window = 1
	}
	return &Reorderer{
		Window:  window,
		pending: make(map[uint64]Packet),
		seen:    make(map[uint64]bool),
	}
}

// Stats returns a copy of the current counters.
func (r *Reorderer) Stats() Stats { return r.stats }

// unwrap extends a 16-bit seq near ref (the highest ext seq seen).
func unwrap(seq uint16, ref uint64) uint64 {
	cand := (ref &^ 0xFFFF) | uint64(seq)
	switch {
	case cand+0x8000 < ref:
		cand += 0x10000
	case ref+0x8000 < cand:
		if cand < 0x10000 {
			return cand // cannot go negative; keep as-is
		}
		cand -= 0x10000
	}
	return cand
}

// Push parses raw as an RTP packet at offset and buffers it. Parse
// failures produce a recoverable diagnostic (the stream continues with
// the next record).
func (r *Reorderer) Push(raw []byte, offset int64) *model.Diag {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		d := model.NewDiag(model.StageRTP, "rtp-parse", offset, true, err.Error())
		return &d
	}
	r.stats.Received++
	ext := uint64(p.SequenceNumber)
	if !r.init {
		r.init = true
		r.highest = ext
		r.nextEmit = ext
	} else {
		ext = unwrap(p.SequenceNumber, r.highest)
	}
	if r.seen[ext] {
		r.stats.Dups++
		return nil
	}
	r.seen[ext] = true
	if ext > r.highest {
		r.highest = ext
	}
	r.pending[ext] = Packet{Offset: offset, Parsed: p, ExtSeq: ext, Payload: p.Payload}
	return nil
}

// Drain finalises every sequence number whose wait window has closed,
// in order. Missing numbers increment the loss counter; present packets
// are returned in emission order. Call repeatedly until empty, and once
// more with flush=true at end of stream.
func (r *Reorderer) Drain(flush bool) []Packet {
	var out []Packet
	for {
		if len(r.pending) == 0 && !flush {
			break
		}
		if r.nextEmit > r.highest {
			break
		}
		if !flush && r.nextEmit+r.Window > r.highest {
			break // still inside the wait window
		}
		if pkt, ok := r.pending[r.nextEmit]; ok {
			out = append(out, pkt)
			delete(r.pending, r.nextEmit)
			r.stats.Unique++
		} else if r.seen[r.nextEmit] {
			// already emitted (cannot happen) — skip
		} else {
			r.stats.Lost++
		}
		r.nextEmit++
	}
	return out
}
