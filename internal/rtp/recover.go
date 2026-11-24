package rtp

import (
	"mediasync/internal/clock"
)

// GapKind classifies a detected sequence-number hole.
type GapKind string

const (
	// GapConcealed: the missing run is short enough to replace with silence
	// placeholders on the nominal timeline.
	GapConcealed GapKind = "concealed"
	// GapBlocked: the missing run is too long or unbounded; no duration can
	// be inferred, so the timeline is explicitly blocked instead of forged.
	GapBlocked GapKind = "blocked"
)

// Gap records one hole between observed sequence numbers.
type Gap struct {
	StartSeq int64
	EndSeq   int64 // inclusive
	Packets  int64
	StartTS  clock.Tick
	EndTS    clock.Tick // exclusive
	Kind     GapKind
}

// Interval is a continuous media region on the repaired timeline. Gaps up
// to MaxConceal are filled with silence and remain part of the interval; a
// blocked gap splits the timeline and ends the preceding interval.
type Interval struct {
	Index     int
	StartSeq  int64
	EndSeq    int64 // inclusive
	StartTS   clock.Tick
	EndTS     clock.Tick // exclusive
	Packets   []*Packet
	Concealed int64
}

// Recoverer buffers reordered RTP packets, extends sequence numbers across
// 16-bit wraparound, deduplicates, and emits continuous intervals with
// silence-concealed or explicitly blocked gaps.
type Recoverer struct {
	ptStep     clock.Tick // nominal timestamp step per packet
	maxConceal int64
	buffer     map[int64]*Packet
	minSeq     int64
	maxSeq     int64
	started    bool
	dupCount   int64
}

// NewRecoverer builds a recoverer. ptStep is the nominal 90 kHz timestamp
// increment per packet (e.g. 900 ticks for 10 ms audio).
func NewRecoverer(ptStep clock.Tick, maxConceal int64) (*Recoverer, error) {
	if ptStep <= 0 || maxConceal < 0 {
		return nil, clock.NewError(clock.ErrInvalidArgument,
			"ptStep=%d maxConceal=%d invalid", ptStep, maxConceal)
	}
	return &Recoverer{ptStep: ptStep, maxConceal: maxConceal,
		buffer: make(map[int64]*Packet), minSeq: 1 << 62, maxSeq: -1 << 62}, nil
}

// Push inserts one parsed packet. Duplicates of an already-seen sequence
// number are counted and ignored.
func (r *Recoverer) Push(p *Packet) error {
	if !r.started {
		// The first observed packet anchors the extended number space.
		p.ExtSeq = int64(p.Seq())
		r.buffer[p.ExtSeq] = p
		r.started, r.minSeq, r.maxSeq = true, p.ExtSeq, p.ExtSeq
		return nil
	}
	seq := clock.ExtendSeq16(r.maxSeq, p.Seq())
	if seq < r.minSeq-(1<<15) {
		return clock.ErrorAt(clock.ErrBlocked, seq, "packet arrives more than half the number space late")
	}
	p.ExtSeq = seq
	if _, exists := r.buffer[seq]; exists {
		r.dupCount++
		return nil
	}
	r.buffer[seq] = p
	if seq < r.minSeq {
		r.minSeq = seq
	}
	if seq > r.maxSeq {
		r.maxSeq = seq
	}
	return nil
}

// Stats describes recovery bookkeeping.
type Stats struct {
	Expected  int64 // distinct sequence positions from min..max inclusive
	Received  int64 // distinct packets actually received
	Duplicate int64
	Lost      int64
	Concealed int64
	Blocked   int64
}

// Flush drains the buffer into continuous intervals and gap records. It is
// deterministic for any given set of pushed packets.
func (r *Recoverer) Flush() ([]Interval, []Gap, Stats) {
	st := Stats{Received: int64(len(r.buffer)), Duplicate: r.dupCount}
	if !r.started {
		return nil, nil, st
	}
	st.Expected = r.maxSeq - r.minSeq + 1
	var intervals []Interval
	var gaps []Gap
	var cur *Interval
	flushCur := func() {
		if cur != nil {
			cur.Index = len(intervals)
			intervals = append(intervals, *cur)
			cur = nil
		}
	}

	for seq := r.minSeq; seq <= r.maxSeq; seq++ {
		p, ok := r.buffer[seq]
		if !ok {
			start := seq
			for seq <= r.maxSeq {
				if _, hole := r.buffer[seq]; hole {
					break
				}
				seq++
			}
			end := seq - 1
			run := end - start + 1
			baseTS := r.gapBaseTS(start)
			g := Gap{StartSeq: start, EndSeq: end, Packets: run,
				StartTS: baseTS, EndTS: baseTS + r.ptStep*clock.Tick(run)}
			st.Lost += run
			if run <= r.maxConceal {
				g.Kind = GapConcealed
				st.Concealed += run
				if cur != nil {
					cur.EndSeq, cur.EndTS = end, g.EndTS
					cur.Concealed += run
				}
			} else {
				g.Kind = GapBlocked
				st.Blocked += run
				flushCur() // un-inferrable duration ends the interval
			}
			gaps = append(gaps, g)
			if seq > r.maxSeq {
				break
			}
			seq-- // outer loop increments back onto the present packet
			continue
		}

		ts := r.packetTS(p)
		if cur == nil || cur.EndSeq+1 != seq {
			flushCur()
			cur = &Interval{StartSeq: seq, EndSeq: seq, StartTS: ts, EndTS: ts + r.ptStep}
		} else {
			cur.EndSeq, cur.EndTS = seq, ts+r.ptStep
		}
		cur.Packets = append(cur.Packets, p)
	}
	flushCur()
	return intervals, gaps, st
}

// gapBaseTS reconstructs the nominal timestamp of the first missing packet
// from its sequence position relative to the stream start.
func (r *Recoverer) gapBaseTS(startSeq int64) clock.Tick {
	if first, ok := r.buffer[r.minSeq]; ok {
		return r.packetTS(first) + r.ptStep*clock.Tick(startSeq-r.minSeq)
	}
	return 0
}

// packetTS extends the 32-bit wire timestamp of p against the first packet,
// using the signed 32-bit modular delta so 32-bit wraparound is resolved.
func (r *Recoverer) packetTS(p *Packet) clock.Tick {
	first := r.buffer[r.minSeq]
	base := clock.ExtendTick32(0, first.TS())
	if p == first {
		return base
	}
	delta := clock.Tick(int32(p.Packet.Timestamp - first.Packet.Timestamp))
	return base + delta
}

// RecoveryRate returns the fraction of lost packets that could be concealed,
// in the range [0,1]. Returns 1 when nothing was lost.
func RecoveryRate(st Stats) float64 {
	if st.Lost == 0 {
		return 1
	}
	return float64(st.Concealed) / float64(st.Lost)
}

// DupCount exposes the number of duplicate packets discarded.
func (r *Recoverer) DupCount() int64 { return r.dupCount }
