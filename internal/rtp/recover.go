package rtp

import (
	pionrtp "github.com/pion/rtp"
)

// Recoverer turns a packet stream into a committed in-order timeline,
// tracking loss, duplication and late arrivals.
type Recoverer struct {
	maxGap  uint64
	started bool
	roc     uint32
	maxSeq  uint16
	next    uint64 // next extended sequence number to commit
	buf     *jitterBuffer

	Stats Stats
}

// NewRecoverer creates a Recoverer tolerating gaps up to maxGap
// sequence numbers before declaring a loss.
func NewRecoverer(maxGap int) *Recoverer {
	if maxGap < 0 {
		maxGap = 0
	}
	return &Recoverer{maxGap: uint64(maxGap), buf: newJitterBuffer()}
}

// extend maps a 16-bit sequence number onto the extended 32-bit-cycle
// sequence space, handling wraparound and pre-wrap stragglers.
func (r *Recoverer) extend(seq uint16) uint64 {
	if !r.started {
		r.started = true
		r.maxSeq = seq
		r.next = uint64(seq)
		return uint64(seq)
	}
	d := int32(seq) - int32(r.maxSeq)
	roc := r.roc
	switch {
	case d >= 0 && d < 30000:
		r.maxSeq = seq
	case d < 0 && d > -30000:
		// Reordered within the current cycle.
	case d <= -30000:
		// Forward wrap into the next cycle.
		roc = r.roc + 1
		r.roc = roc
		r.maxSeq = seq
	default:
		// d >= 30000: straggler from the previous cycle.
		if r.roc > 0 {
			roc = r.roc - 1
		}
	}
	return uint64(roc)<<16 | uint64(seq)
}

// Feed ingests one packet and returns any packets committed in order
// as a result. Duplicates and late packets are counted and discarded.
func (r *Recoverer) Feed(pkt *pionrtp.Packet) []Committed {
	r.Stats.Received++
	ext := r.extend(pkt.SequenceNumber)
	if ext < r.next {
		// The position is already committed; a late packet must not
		// overwrite the committed timeline.
		if r.wasLost(ext) {
			r.Stats.Late++
		} else {
			r.Stats.Duplicated++
		}
		return nil
	}
	if _, ok := r.buf.pkts[ext]; ok {
		r.Stats.Duplicated++
		return nil
	}
	r.buf.pkts[ext] = pkt
	return r.commit(false)
}

// Flush commits everything still buffered, declaring any remaining
// gaps lost. Call once at end of input.
func (r *Recoverer) Flush() []Committed {
	return r.commit(true)
}

func (r *Recoverer) commit(flush bool) []Committed {
	var out []Committed
	for {
		if pkt, ok := r.buf.pkts[r.next]; ok {
			out = append(out, Committed{Ext: r.next, Packet: pkt})
			delete(r.buf.pkts, r.next)
			r.next++
			continue
		}
		m, ok := r.buf.min()
		if !ok {
			return out
		}
		if flush || m-r.next > r.maxGap {
			r.declareLost(r.next, m-1)
			r.recordRecovery(m)
			r.next = m
			continue
		}
		return out
	}
}

func (r *Recoverer) declareLost(from, to uint64) {
	r.Stats.Lost += int(to - from + 1)
	r.Stats.Missing = append(r.Stats.Missing, Range{From: from, To: to})
}

func (r *Recoverer) recordRecovery(seq uint64) {
	rec := Recovery{Seq: seq}
	if pkt, ok := r.buf.pkts[seq]; ok {
		rec.RTPTimestamp = pkt.Timestamp
	}
	r.Stats.Recoveries = append(r.Stats.Recoveries, rec)
}

func (r *Recoverer) wasLost(ext uint64) bool {
	for _, m := range r.Stats.Missing {
		if ext >= m.From && ext <= m.To {
			return true
		}
	}
	return false
}
