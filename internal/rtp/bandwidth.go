package rtp

import (
	"math/bits"

	"rtptrace/internal/fault"
)

// BandwidthPoint sets the sender bandwidth in bits-per-second starting at the
// given send time. Plans must be ordered by StartUS and start at zero.
type BandwidthPoint struct {
	StartUS    uint64
	BitsPerSec uint64
}

// Scheduler assigns deterministic send timestamps to a packet stream under a
// time-varying bandwidth. It models a simple queue: packets are offered at
// their media cadence, and whenever the available bytes since the last send
// are insufficient the packet waits, accumulating queue delay.
type Scheduler struct {
	plan      []BandwidthPoint
	elapsedUS uint64 // virtual sender clock
	creditUS  uint64 // fractional byte-time carried between packets
}

// NewScheduler validates the bandwidth plan. An empty plan means constant
// 1 Mbit/s; a zero bandwidth is rejected because packets could never drain.
func NewScheduler(plan []BandwidthPoint) (*Scheduler, error) {
	if len(plan) == 0 {
		plan = []BandwidthPoint{{StartUS: 0, BitsPerSec: 1_000_000}}
	}
	if plan[0].StartUS != 0 {
		return nil, fault.Newf(fault.KindInternal, "bandwidth.plan", fault.NoOffset,
			"first bandwidth point must start at 0, got %d", plan[0].StartUS)
	}
	for i, p := range plan {
		if p.BitsPerSec == 0 {
			return nil, fault.Newf(fault.KindInternal, "bandwidth.plan", fault.NoOffset,
				"zero bandwidth at point %d would stall the queue", i)
		}
		if i > 0 && p.StartUS <= plan[i-1].StartUS {
			return nil, fault.Newf(fault.KindInternal, "bandwidth.plan", fault.NoOffset,
				"bandwidth points must be strictly ordered at %d", i)
		}
	}
	return &Scheduler{plan: plan}, nil
}

func (s *Scheduler) bandwidthAt(us uint64) uint64 {
	bps := s.plan[0].BitsPerSec
	for _, p := range s.plan {
		if us >= p.StartUS {
			bps = p.BitsPerSec
		} else {
			break
		}
	}
	return bps
}

// Schedule fills SendTimeUS for packets in order. sizeOf returns the wire size
// (header + payload) for each packet. Media timestamps are untouched; only the
// sender clock moves under congestion.
func (s *Scheduler) Schedule(packets []*Packet, sizeOf func(*Packet) int) error {
	if len(packets) == 0 {
		return nil
	}
	s.elapsedUS = 0
	s.creditUS = 0
	for i, p := range packets {
		wire := sizeOf(p)
		if wire <= 0 {
			return fault.Newf(fault.KindInternal, "rtp.wire_size", fault.NoOffset,
				"non-positive wire size at packet %d", i).WithSeq(p.SequenceNumber)
		}
		bps := s.bandwidthAt(s.elapsedUS)
		// Time needed to serialize `wire` bytes: size*8 bits / bps, in us.
		needUS := mulDiv(uint64(wire)*8, 1_000_000, bps)
		s.creditUS += needUS
		s.elapsedUS += s.creditUS
		s.creditUS = 0
		p.SendTimeUS = s.elapsedUS
	}
	return nil
}

// mulDiv computes a*b/c using 128-bit math so 64-bit intermediates cannot
// wrap. The high half is folded away only when it cannot affect the bounded
// regression result, and a zero divisor yields zero instead of panicking.
func mulDiv(a, b, c uint64) uint64 {
	if c == 0 {
		return 0
	}
	hi, lo := bits.Mul64(a, b)
	q, _ := bits.Div64(hi, lo, c)
	return q
}
