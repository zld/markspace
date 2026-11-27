// Package rtprecover reorders, deduplicates and gap-detects RTP packets per
// SSRC. It never fabricates audio: packets that cannot be recovered inside
// the reorder window are marked lost and surfaced explicitly.
package rtprecover

import (
	"github.com/pion/rtp"

	"mediatool/internal/clock"
	merr "mediatool/internal/errors"
)

// Stats summarise recovery behaviour for one SSRC.
type Stats struct {
	Received     uint64 `json:"received"`
	Duplicates   uint64 `json:"duplicates"`
	Reordered    uint64 `json:"reordered"`
	Lost         uint64 `json:"lost"`
	Recoverable  uint64 `json:"gaps_recovered"`
	LateRejected uint64 `json:"late_outside_window"`
}

// Packet is the minimal recovered unit.
type Packet struct {
	SSRC      uint32
	Seq       uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
}

// Gap records a sequence range that could not be recovered in time.
type Gap struct {
	Start uint16
	End   uint16
}

// Window is a bounded per-SSRC reorder buffer.
//
// Invariants:
//   - base is the unwrapped sequence of the next contiguous deliverable.
//   - highest is the greatest sequence ever accepted (high-water mark).
//   - a packet filling a hole in (base, highest] is a reordered recovery;
//   - a position is declared lost only when overflow forces eviction, or at
//     Finalize, never speculatively.
type Window struct {
	ssrc     uint32
	capacity int
	buf      map[uint16]*Packet
	base     uint32
	highest  uint32
	baseInit bool
	unwrap   *clock.Unwrapper16

	stats         Stats
	unrecoverable []Gap
}

// NewWindow creates a window for ssrc with the given packet capacity.
// A capacity below 2 is raised to 2.
func NewWindow(ssrc uint32, capacity int) *Window {
	if capacity < 2 {
		capacity = 2
	}
	return &Window{
		ssrc:     ssrc,
		capacity: capacity,
		buf:      make(map[uint16]*Packet),
		unwrap:   clock.NewUnwrapper16(),
	}
}

// Push accepts one packet. Duplicates are counted and dropped; packets that
// arrive after their position has left the window are rejected with a
// late_outside_window error and never re-enter the stream.
func (w *Window) Push(p *rtp.Packet) error {
	if p.SSRC != w.ssrc {
		return merr.New(merr.CodeSSRCMismatch,
			"packet SSRC %#x does not match window SSRC %#x", p.SSRC, w.ssrc)
	}
	w.stats.Received++

	ext, err := w.unwrap.Unwrap(p.SequenceNumber)
	if err != nil {
		return err
	}
	if w.baseInit && ext < w.base {
		w.stats.LateRejected++
		return merr.At(merr.CodeLateOutsideWindow, int64(p.SequenceNumber),
			"seq %d arrived after base %d slid past it", p.SequenceNumber, uint16(w.base))
	}
	if _, exists := w.buf[p.SequenceNumber]; exists {
		w.stats.Duplicates++
		return nil
	}
	if w.baseInit && ext <= w.highest && ext > w.base {
		// Filling a hole inside the high-water mark: a reordered recovery.
		w.stats.Reordered++
		w.stats.Recoverable++
	}
	w.buf[p.SequenceNumber] = &Packet{
		SSRC: p.SSRC, Seq: p.SequenceNumber, Timestamp: p.Timestamp,
		Marker: p.Marker, Payload: append([]byte(nil), p.Payload...),
	}
	if !w.baseInit {
		w.base, w.highest, w.baseInit = ext, ext, true
	} else if ext > w.highest {
		w.highest = ext
	}
	for len(w.buf) > w.capacity {
		w.evictOne()
	}
	return nil
}

// evictOne makes room when the window overflows. A contiguous head is
// delivered cheaply; a missing head forces the oldest gap to be declared
// lost. No placeholder packet is ever inserted.
func (w *Window) evictOne() {
	if _, ok := w.buf[uint16(w.base)]; ok {
		delete(w.buf, uint16(w.base))
		w.base++
		return
	}
	lowest := w.lowestBufferedExt()
	w.declareLost(w.base, lowest-1)
}

func (w *Window) declareLost(from, to uint32) {
	w.stats.Lost += uint64(to - from + 1)
	w.unrecoverable = append(w.unrecoverable, Gap{Start: uint16(from), End: uint16(to)})
	for seq := from; seq <= to; seq++ {
		delete(w.buf, uint16(seq))
	}
	w.base = to + 1
}

// Drain returns all currently contiguous packets in sequence order, advancing
// the base over them. A gap stops the drain; it is not fabricated.
func (w *Window) Drain() []*Packet {
	out := make([]*Packet, 0, len(w.buf))
	for {
		p, ok := w.buf[uint16(w.base)]
		if !ok {
			break
		}
		out = append(out, p)
		delete(w.buf, uint16(w.base))
		w.base++
	}
	if w.base > w.highest {
		w.highest = w.base - 1
	}
	return out
}

// Finalize flushes the window at end of stream. Remaining gaps become lost;
// buffered packets are returned in order.
func (w *Window) Finalize() []*Packet {
	out := make([]*Packet, 0, len(w.buf))
	for len(w.buf) > 0 {
		if p, ok := w.buf[uint16(w.base)]; ok {
			out = append(out, p)
			delete(w.buf, uint16(w.base))
			w.base++
			continue
		}
		// Base is missing: give up the gap up to the next buffered packet.
		lowest := w.lowestBufferedExt()
		w.declareLost(w.base, lowest-1)
	}
	w.highest = w.base - 1
	return out
}

func (w *Window) lowestBufferedExt() uint32 {
	first := true
	var lowest uint32
	for seq := range w.buf {
		ext, err := w.unwrap.Unwrap(seq)
		if err != nil {
			continue
		}
		if first || ext < lowest {
			lowest, first = ext, false
		}
	}
	return lowest
}

// Stats returns a copy of the running counters.
func (w *Window) Stats() Stats { return w.stats }

// Unrecoverable returns the gaps given up during processing.
func (w *Window) Unrecoverable() []Gap { return append([]Gap(nil), w.unrecoverable...) }
