// Package rtp turns parsed media samples into an RTP trace and models the
// weak-network effects seen in offline regression: duplicated delivery,
// reordering inside a bounded window, and burst loss outside it.
//
// The media timestamp of every packet is derived from the container time base
// and never changes; the bandwidth subsystem only reshapes send timing.
package rtp

import (
	"github.com/pion/rtp"

	"rtptrace/internal/fault"
)

// Packet is one delivered RTP packet plus the weak-network bookkeeping the
// regression needs. Raw is the canonical pion encoding used for artifacts.
type Packet struct {
	SSRC           uint32
	SequenceNumber uint16
	Timestamp      uint32
	PayloadType    uint8
	Marker         bool
	Payload        []byte
	Raw            []byte
	SendTimeUS     uint64 // scheduled send time in microseconds (bandwidth only)
	Duplicate      bool   // true when this delivery repeated an earlier seq
}

// Gap records a contiguous run of sequence numbers that never arrived inside
// the reorder window, i.e. a loss event.
type Gap struct {
	SSRC  uint32
	Start uint16
	Count int
}

// Stats summarizes one reordering pass. Counters are stable across reruns
// because processing is fully deterministic.
type Stats struct {
	Received    int // unique accepted packets
	Duplicates  int
	LostPackets int
	Reordered   int // packets delivered out of strict seq order
	Emitted     int // packets handed to the output in order
}

// Window is a per-SSRC bounded reorder buffer. Packets arriving out of order
// within the window are buffered and released in sequence order; a missing
// sequence number older than the window is finalized as a loss gap.
type Window struct {
	ssrc     uint32
	capacity int
	base     uint16 // next expected sequence number
	started  bool
	buf      map[uint16]*Packet
	stats    Stats
	gaps     []Gap
}

// NewWindow builds a reorder window for one SSRC. capacity must be positive.
func NewWindow(ssrc uint32, capacity int) (*Window, error) {
	if capacity <= 0 {
		return nil, fault.Newf(fault.KindInternal, "window.capacity", fault.NoOffset,
			"capacity must be positive, got %d", capacity)
	}
	return &Window{ssrc: ssrc, capacity: capacity, buf: map[uint16]*Packet{}}, nil
}

// Push accepts one delivery. A repeated (ssrc, seq) is counted as a duplicate
// and ignored; an in-order packet flushes everything contiguous, while a jump
// beyond the window finalizes the skipped numbers as loss. It returns the
// packets that became releasable in sequence order as a result of this push.
func (w *Window) Push(p *Packet) ([]*Packet, error) {
	if p.SSRC != w.ssrc {
		return nil, fault.Newf(fault.KindInternal, "packet.ssrc", fault.NoOffset,
			"window ssrc=%d got packet ssrc=%d", w.ssrc, p.SSRC).WithSeq(p.SequenceNumber)
	}
	if !w.started {
		w.base = p.SequenceNumber
		w.started = true
	}
	// Anything at or behind the next-expected seq, or already buffered, is a
	// duplicate delivery: its first occurrence was accepted earlier.
	dist := seqDelta(p.SequenceNumber, w.base)
	if dist < 0 {
		return nil, w.markDuplicate(p)
	}
	if _, exists := w.buf[p.SequenceNumber]; exists {
		return nil, w.markDuplicate(p)
	}

	// A forward jump larger than the window means the skipped packets will
	// never be reordered back; finalize them as loss immediately.
	var orphaned []*Packet
	if dist > w.capacity {
		orphaned = w.finalizeLoss(w.base, p.SequenceNumber)
	} else if dist > 0 {
		w.stats.Reordered++
	}

	w.buf[p.SequenceNumber] = p
	w.stats.Received++
	out := w.flushContiguous()
	return append(orphaned, out...), nil
}

// flushContiguous emits every buffered packet starting at base in order,
// advancing base. It is the single place where base moves on real packets.
func (w *Window) flushContiguous() []*Packet {
	var out []*Packet
	for {
		p, ok := w.buf[w.base]
		if !ok {
			return out
		}
		delete(w.buf, w.base)
		out = append(out, p)
		w.base++
		w.stats.Emitted++
	}
}

// PushInto is Push for callers that accumulate output across pushes, such as
// the multi-SSRC reassembler; released packets are appended to sink in order.
func (w *Window) PushInto(p *Packet, sink *[]*Packet) error {
	out, err := w.Push(p)
	if err != nil {
		return err
	}
	*sink = append(*sink, out...)
	return nil
}

// Drain emits any packets that are currently contiguous without new input.
func (w *Window) Drain() []*Packet {
	return w.flushContiguous()
}

func (w *Window) markDuplicate(p *Packet) error {
	p.Duplicate = true
	w.stats.Duplicates++
	return nil
}

// seqDelta returns the signed forward distance from base to seq in 16-bit
// sequence space, correctly handling wraparound (range [-32768, 32767]).
func seqDelta(seq, base uint16) int {
	return int(int16(seq - base))
}

// finalizeLoss records every missing seq in [from, to) as contiguous gaps,
// advances base to to, and returns already-buffered surviving packets inside
// the span in sequence order. Those survivors arrived early while the gap was
// still treated as a recoverable reorder; promoting them keeps them from being
// stranded behind the new base.
func (w *Window) finalizeLoss(from, to uint16) []*Packet {
	missing := seqDelta(to, from)
	if missing <= 0 {
		w.base = to
		return nil
	}
	surv := map[uint16]*Packet{}
	for d := 0; d < missing; d++ {
		seq := from + uint16(d)
		if p, ok := w.buf[seq]; ok {
			surv[seq] = p
		}
	}
	// Record the non-survivor members of the span as contiguous gaps.
	runStart := from
	run := 0
	flushRun := func() {
		if run == 0 {
			return
		}
		if n := len(w.gaps); n > 0 && w.gaps[n-1].SSRC == w.ssrc &&
			uint16(int(w.gaps[n-1].Start)+w.gaps[n-1].Count) == runStart {
			w.gaps[n-1].Count += run
		} else {
			w.gaps = append(w.gaps, Gap{SSRC: w.ssrc, Start: runStart, Count: run})
		}
		run = 0
	}
	for seq := from; seq != to; seq++ {
		if p, ok := surv[seq]; ok {
			flushRun()
			delete(w.buf, seq)
			w.stats.Emitted++
			_ = p
			continue
		}
		if run == 0 {
			runStart = seq
		}
		run++
	}
	flushRun()
	w.stats.LostPackets += missing - len(surv)

	// Return survivors ordered by sequence.
	survivors := make([]*Packet, 0, len(surv))
	for d := 0; d < missing; d++ {
		if p, ok := surv[from+uint16(d)]; ok {
			survivors = append(survivors, p)
		}
	}
	w.base = to
	return survivors
}

// Flush marks sequence numbers still missing at end of stream as trailing loss
// and returns the final stats together with all recorded gaps. Only the span
// from the current base up to the highest buffered sequence is considered;
// packets already released by flushContiguous are never revisited.
func (w *Window) Flush() (Stats, []Gap) {
	if w.started && len(w.buf) > 0 {
		// Highest buffered seq is the stream frontier relative to base.
		var maxSeq uint16
		maxDist := 0
		first := true
		for seq := range w.buf {
			d := seqDelta(seq, w.base)
			if first || d > maxDist {
				maxDist = d
				maxSeq = seq
				first = false
			}
		}
		span := seqDelta(maxSeq, w.base) // bounded by window size, never huge
		target := w.base + uint16(span+1)
		var runStart uint16
		run := 0
		for seq := w.base; seq != target; seq++ {
			if _, ok := w.buf[seq]; ok {
				if run > 0 {
					w.recordTrailing(runStart, run)
					run = 0
				}
				continue
			}
			if run == 0 {
				runStart = seq
			}
			run++
		}
		if run > 0 {
			w.recordTrailing(runStart, run)
		}
	}
	return w.stats, append([]Gap(nil), w.gaps...)
}

func (w *Window) recordTrailing(start uint16, count int) {
	if n := len(w.gaps); n > 0 && w.gaps[n-1].SSRC == w.ssrc &&
		uint16(int(w.gaps[n-1].Start)+w.gaps[n-1].Count) == start {
		w.gaps[n-1].Count += count
	} else {
		w.gaps = append(w.gaps, Gap{SSRC: w.ssrc, Start: start, Count: count})
	}
	w.stats.LostPackets += count
}

// Encode canonicalizes a packet through pion so Raw bytes are stable.
func Encode(p *Packet) error {
	rp := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         p.Marker,
			PayloadType:    p.PayloadType,
			SequenceNumber: p.SequenceNumber,
			Timestamp:      p.Timestamp,
			SSRC:           p.SSRC,
		},
		Payload: p.Payload,
	}
	raw, err := rp.Marshal()
	if err != nil {
		return fault.Newf(fault.KindInternal, "rtp.marshal", fault.NoOffset,
			"seq=%d: %v", p.SequenceNumber, err).WithSeq(p.SequenceNumber)
	}
	p.Raw = raw
	return nil
}
