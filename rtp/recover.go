package rtp

import (
	"github.com/pion/rtp"

	"mediacore/media"
)

// Recovered is one emitted item in extended (wrap-free) sequence space.
// Concealed items repeat the previous payload to bridge a loss burst;
// their TS repeats the last known timestamp.
type Recovered struct {
	Packet    *rtp.Packet
	Seq       int64 // extended sequence number
	TS        int64 // extended RTP timestamp
	Concealed bool
}

// Stats reports what recovery did, for the pipeline's metrics.
type Stats struct {
	Received   int
	Duplicates int
	Reordered  int
	Recovered  int // missing packets concealed
	Lost       int // missing packets beyond concealment
	MaxBurst   int // longest loss burst seen
}

// Recover reorders and dedups packets, unwraps sequence numbers and
// timestamps, and conceals loss bursts up to maxConceal packets. Longer
// bursts are reported lost, never silently bridged. Memory is bounded by
// the reorder window.
type Recover struct {
	window     int
	maxConceal int
	seqU       media.SeqUnwrapper
	tsU        media.Unwrapper
	pending    map[int64]*rtp.Packet
	head       int64
	maxSeen    int64
	started    bool
	last       *rtp.Packet
	lastTS     int64
	burst      int
	Stats      Stats
}

// NewRecover builds a Recover with the given reorder window (packets)
// and concealment limit.
func NewRecover(window, maxConceal int) *Recover {
	return &Recover{window: window, maxConceal: maxConceal, pending: make(map[int64]*rtp.Packet)}
}

// Seed primes concealment state with the last emitted packet, so a
// pipeline resuming mid-stream conceals an immediate gap with the same
// payload an uninterrupted run would have used.
func (r *Recover) Seed(last *rtp.Packet, ts int64) {
	r.last = last
	r.lastTS = ts
}

// Push feeds one packet (wire order, possibly lossy/reordered/duplicated)
// and returns everything that became emit-ready.
func (r *Recover) Push(pkt *rtp.Packet) []Recovered {
	eseq := r.seqU.Unwrap(pkt.SequenceNumber)
	if !r.started {
		r.started, r.head, r.maxSeen = true, eseq, eseq
	}
	if eseq < r.head {
		r.Stats.Duplicates++ // already emitted; a true duplicate
		return nil
	}
	if eseq < r.maxSeen {
		r.Stats.Reordered++
	}
	if eseq > r.maxSeen {
		r.maxSeen = eseq
	}
	if _, dup := r.pending[eseq]; dup {
		r.Stats.Duplicates++
		return nil
	}
	r.pending[eseq] = pkt
	r.Stats.Received++
	return r.drain(false)
}

// Flush ends the stream: remaining buffered packets are emitted, gaps
// between them concealed or counted lost.
func (r *Recover) Flush() []Recovered {
	return r.drain(true)
}

func (r *Recover) drain(flush bool) []Recovered {
	var out []Recovered
	for {
		if pkt, ok := r.pending[r.head]; ok {
			delete(r.pending, r.head)
			r.lastTS = r.tsU.Unwrap(pkt.Timestamp)
			r.last = pkt
			out = append(out, Recovered{Packet: pkt, Seq: r.head, TS: r.lastTS})
			r.head++
			r.burst = 0
			continue
		}
		next, ok := r.nextBuffered()
		if !ok {
			break // nothing ahead; wait for more packets
		}
		if !flush && int(len(r.pending)) <= r.window {
			break // gap may still fill; window not yet exhausted
		}
		out = append(out, r.bridge(next-r.head)...)
	}
	return out
}

// bridge spans a gap of n missing sequence numbers: conceal up to
// maxConceal by repeating the last payload, count the rest lost.
func (r *Recover) bridge(n int64) []Recovered {
	var out []Recovered
	for i := int64(0); i < n; i++ {
		r.burst++
		if r.burst > r.Stats.MaxBurst {
			r.Stats.MaxBurst = r.burst
		}
		if n <= int64(r.maxConceal) && r.last != nil {
			cp := *r.last
			cp.SequenceNumber = uint16(r.head)
			out = append(out, Recovered{Packet: &cp, Seq: r.head, TS: r.lastTS, Concealed: true})
			r.Stats.Recovered++
		} else {
			r.Stats.Lost++
		}
		r.head++
	}
	return out
}

func (r *Recover) nextBuffered() (int64, bool) {
	best := int64(-1)
	for k := range r.pending {
		if k > r.head && (best < 0 || k < best) {
			best = k
		}
	}
	return best, best >= 0
}
