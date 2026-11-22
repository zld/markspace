package transport

import "fmt"

// Gap records an unrecoverable run of missing sequence numbers and the RTP
// timestamp interval they span. Media data is never fabricated for a gap.
type Gap struct {
	StartSeq  uint64
	EndSeq    uint64 // inclusive
	StartTime uint32 // RTP timestamp bounding the gap (last seen before)
	EndTime   uint32 // first timestamp seen after the gap (0 if unknown)
}

// Stats summarizes buffer activity for the manifest.
type Stats struct {
	Received   int // unique packets accepted
	Duplicates int // idempotently dropped
	Lost       int // packets declared unrecoverable
	Recovered  int // out-of-order packets emitted in correct position
	Gaps       []Gap
}

// JitterBuffer reorders packets within a fixed window. A missing sequence
// number is declared lost only when the window has advanced past it.
type JitterBuffer struct {
	window  uint64
	buf     map[uint64]StreamPacket
	next    uint64 // next sequence to emit
	hiPush  uint64 // highest sequence ever pushed
	started bool
	stats   Stats
}

// NewJitterBuffer creates a buffer; window must be in (0, MaxWindow].
func NewJitterBuffer(window uint64) (*JitterBuffer, error) {
	if window == 0 || window > MaxWindow {
		return nil, fmt.Errorf("transport: jitter window %d out of range (0,%d]", window, MaxWindow)
	}
	return &JitterBuffer{window: window, buf: make(map[uint64]StreamPacket)}, nil
}

// Stats returns a copy of the current counters.
func (jb *JitterBuffer) Stats() Stats { return jb.stats }

// Push inserts a packet. Duplicates are dropped idempotently; packets at or
// below the already-emitted frontier are dropped as stale duplicates.
func (jb *JitterBuffer) Push(p StreamPacket) {
	if !jb.started {
		jb.started = true
		jb.next = p.ExtSeq
	}
	if p.ExtSeq < jb.next {
		jb.stats.Duplicates++
		return
	}
	if _, exists := jb.buf[p.ExtSeq]; exists {
		jb.stats.Duplicates++
		return
	}
	// Late = arrived after a higher sequence was already seen (true
	// out-of-order); only such packets count as recovered when emitted.
	p.Late = p.ExtSeq < jb.hiPush
	if p.ExtSeq > jb.hiPush {
		jb.hiPush = p.ExtSeq
	}
	jb.buf[p.ExtSeq] = p
	jb.stats.Received++
}

// Next emits the next in-order packet. When the buffered frontier has moved
// a full window past a missing sequence number, the hole is declared lost,
// recorded as a gap, and skipped. Returns ok=false when no packet can be
// emitted yet.
func (jb *JitterBuffer) Next() (pkt StreamPacket, ok bool) {
	if !jb.started {
		return pkt, false
	}
	p, exists := jb.buf[jb.next]
	if !exists {
		if !jb.windowExceeded() {
			return pkt, false
		}
		jb.declareGaps()
		p, exists = jb.buf[jb.next]
		if !exists {
			return pkt, false
		}
	}
	delete(jb.buf, jb.next)
	jb.next++
	if p.Late {
		jb.stats.Recovered++
	}
	return p, true
}

// Flush drains the buffer at end of stream, declaring any remaining holes
// lost. Returns emitted packets in order.
func (jb *JitterBuffer) Flush() []StreamPacket {
	var out []StreamPacket
	for len(jb.buf) > 0 {
		if p, ok := jb.buf[jb.next]; ok {
			delete(jb.buf, jb.next)
			if p.Late {
				jb.stats.Recovered++
			}
			out = append(out, p)
		} else {
			jb.stats.Lost++
		}
		jb.next++
	}
	return out
}

// windowExceeded reports whether the highest buffered sequence is at least
// one full window beyond the next expected sequence.
func (jb *JitterBuffer) windowExceeded() bool {
	var hi uint64
	for s := range jb.buf {
		if s > hi {
			hi = s
		}
	}
	return len(jb.buf) > 0 && hi >= jb.next+jb.window
}

// declareGaps converts the hole run between next and the first buffered
// packet into a Gap record and advances next past it. Called only when the
// window has been exceeded, so every hole before the first buffered packet
// is unrecoverable.
func (jb *JitterBuffer) declareGaps() {
	for len(jb.buf) > 0 {
		if _, ok := jb.buf[jb.next]; ok {
			return
		}
		if !jb.windowExceeded() {
			return
		}
		start := jb.next
		for {
			if _, ok := jb.buf[jb.next]; ok {
				break
			}
			jb.next++
		}
		end := jb.next - 1
		jb.stats.Gaps = append(jb.stats.Gaps, Gap{
			StartSeq: start, EndSeq: end,
			EndTime: jb.buf[jb.next].Timestamp,
		})
		jb.stats.Lost += int(end - start + 1)
	}
}
