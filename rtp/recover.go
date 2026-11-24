// Package rtp reconstructs an ordered packet stream from lossy RTP return
// feeds using sequence-number arithmetic and a bounded replay window. Gaps
// older than the window are declared unrecoverable and tied to a timeline
// interval rather than buffered without limit.
package rtp

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediacheck/timeline"
)

// DefaultWindow is the maximum out-of-order distance the reorder buffer
// tolerates; gaps older than the window cannot be recovered.
const DefaultWindow = 64

// Stats summarises one recovery run for the demo report.
type Stats struct {
	Received      int // unique packets accepted
	Duplicates    int // repeated sequence numbers, never counted twice
	Lost          int // recovered + unrecoverable
	Recovered     int // initially missing but filled inside the window
	Unrecoverable int // gaps that aged out of the window
	LostIntervals []LostInterval
}

// LostInterval binds an unrecoverable sequence-number gap to the clock.
type LostInterval struct {
	SeqStart uint16
	SeqEnd   uint16
	StartNs  int64
	EndNs    int64
}

// RecoveryRate returns recovered / (recovered + unrecoverable); 1 when clean.
func (s Stats) RecoveryRate() float64 {
	recoverable := s.Recovered + s.Unrecoverable
	if recoverable == 0 {
		return 1
	}
	return float64(s.Recovered) / float64(recoverable)
}

// Receiver performs bounded reordering and loss accounting.
type Receiver struct {
	window   int
	tickRate int64

	initialized bool
	baseSeq     uint16 // oldest sequence not yet released or aged out
	highestSeq  uint16 // highest sequence ever accepted
	buf         map[uint16]*rtp.Packet
	// missing holds sequence numbers seen as gaps but not yet resolved.
	missing map[uint16]bool

	stats Stats
}

// NewReceiver builds a receiver with the given window bound and clock rate.
func NewReceiver(window, tickRate int64) *Receiver {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Receiver{
		window:   int(window),
		tickRate: tickRate,
		buf:      make(map[uint16]*rtp.Packet),
		missing:  make(map[uint16]bool),
	}
}

// Handle ingests one raw RTP datagram. Duplicates are counted and ignored,
// in-window reordering is buffered, and a packet beyond the window forces the
// intervening open gaps to be declared unrecoverable before it is accepted.
func (r *Receiver) Handle(raw []byte) error {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return timeline.New(timeline.CatCorrupt, "invalid rtp packet").Wrapf(err)
	}
	seq := pkt.SequenceNumber

	if !r.initialized {
		r.baseSeq, r.highestSeq, r.initialized = seq, seq, true
	} else if seqDistance(r.baseSeq, seq) < 0 {
		// Older than the release horizon: a late duplicate of history.
		r.stats.Duplicates++
		return nil
	}

	if _, buffered := r.buf[seq]; buffered {
		r.stats.Duplicates++
		return nil
	}

	if r.missing[seq] {
		// The missing packet arrived while still inside the window.
		delete(r.missing, seq)
		r.stats.Recovered++
	}

	r.buf[seq] = pkt
	r.stats.Received++

	if seqDistance(r.highestSeq, seq) > 0 {
		// Register every sequence between the old high-water mark and seq.
		for probe := r.highestSeq + 1; probe != seq; probe++ {
			if _, ok := r.buf[probe]; !ok {
				r.missing[probe] = true
			}
		}
		r.highestSeq = seq
	}

	// After registering the new gaps, age out any beyond the bounded window.
	if d := seqDistance(r.baseSeq, seq); d > r.window {
		r.expireBefore(seq)
	}
	return nil
}

// expireBefore declares every still-open gap older than arrival
// unrecoverable, merges consecutive gaps into timeline intervals, and moves
// the release base forward so buffered packets can subsequently drain.
func (r *Receiver) expireBefore(arrival uint16) {
	var aged []uint16
	for seq := range r.missing {
		if seqDistance(seq, arrival) > 0 && seqDistance(r.baseSeq, seq) >= 0 {
			aged = append(aged, seq)
		}
	}
	if len(aged) == 0 {
		r.baseSeq = arrival
		return
	}
	sort.Slice(aged, func(i, j int) bool { return seqDistance(aged[0], aged[i]) < seqDistance(aged[0], aged[j]) })
	var start, end uint16
	var startNs, endNs int64
	flush := func() {
		r.stats.LostIntervals = append(r.stats.LostIntervals, LostInterval{
			SeqStart: start, SeqEnd: end, StartNs: startNs, EndNs: endNs,
		})
	}
	for i, seq := range aged {
		delete(r.missing, seq)
		r.stats.Unrecoverable++
		if i == 0 || seq != end+1 {
			if i > 0 {
				flush()
			}
			start, startNs = seq, r.seqToNs(seq)
		}
		end, endNs = seq, r.seqToNs(seq)
	}
	flush()
	r.baseSeq = arrival
}

// Drain releases every contiguous packet available from the base in order.
func (r *Receiver) Drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		pkt, ok := r.buf[r.baseSeq]
		if !ok {
			break
		}
		out = append(out, pkt)
		delete(r.buf, r.baseSeq)
		r.baseSeq++
	}
	return out
}

// Flush ages out every open gap and returns the remaining buffered packets.
func (r *Receiver) Flush() []*rtp.Packet {
	if len(r.missing) > 0 {
		r.expireBefore(r.highestSeq + 1)
	}
	return r.Drain()
}

// Stats returns a copy of the running counters with the derived loss total.
func (r *Receiver) Stats() Stats {
	s := r.stats
	s.Lost = s.Recovered + s.Unrecoverable
	return s
}

func (r *Receiver) seqToNs(seq uint16) int64 {
	if r.tickRate <= 0 {
		return 0
	}
	ns, ok := timeline.NsFromUnits(int64(seq), r.tickRate)
	if !ok {
		return 0
	}
	return ns
}

// seqDistance returns b-a treating sequence numbers as modular uint16.
func seqDistance(a, b uint16) int {
	d := int(b) - int(a)
	if d > 32767 {
		d -= 65536
	} else if d < -32768 {
		d += 65536
	}
	return d
}

func (s Stats) String() string {
	return fmt.Sprintf("recv=%d dup=%d lost=%d recovered=%d unrecoverable=%d",
		s.Received, s.Duplicates, s.Lost, s.Recovered, s.Unrecoverable)
}
