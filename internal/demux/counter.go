package demux

import (
	"sort"

	"tsinspect/internal/ts"
)

// Issue classifies what a packet implies for continuity state.
type Issue int

const (
	IssueNone Issue = iota
	IssueDuplicate
	IssueCCJump
	IssueDiscontinuity
	IssueTransportError
)

// NullPID carries MPEG-TS null packets.
const NullPID uint16 = 0x1fff

// PIDStat is the per-PID accounting surfaced in reports.
type PIDStat struct {
	PID             uint16
	Packets         int64
	PayloadPackets  int64
	AdaptationOnly  int64
	ContinuityErrs  int64
	Duplicates      int64
	Discontinuities int64
	TransportErrors int64
	LostPackets     int64
}

type pidState struct {
	lastCC  uint8
	hasLast bool
	stat    PIDStat
}

// Counter tracks per-PID packet counts and continuity anomalies. Packets
// without payload never advance the expected continuity counter, and the
// discontinuity indicator suppresses a jump, matching ISO/IEC 13818-1.
type Counter struct {
	pids map[uint16]*pidState
}

// NewCounter creates an empty counter.
func NewCounter() *Counter {
	return &Counter{pids: make(map[uint16]*pidState)}
}

func (c *Counter) state(pid uint16) *pidState {
	s := c.pids[pid]
	if s == nil {
		s = &pidState{stat: PIDStat{PID: pid}}
		c.pids[pid] = s
	}
	return s
}

// Observe accounts for one packet and returns its continuity classification.
func (c *Counter) Observe(p *ts.Packet) Issue {
	s := c.state(p.PID)
	s.stat.Packets++
	if p.HasPayload {
		s.stat.PayloadPackets++
	} else {
		s.stat.AdaptationOnly++
	}
	if p.TransportError {
		s.stat.TransportErrors++
		// A corrupt packet must not become the continuity baseline.
		s.hasLast = false
		return IssueTransportError
	}
	if p.Discontinuity {
		// Signalled discontinuity: resynchronise without reporting a jump.
		s.stat.Discontinuities++
		if p.HasPayload {
			s.lastCC = p.ContinuityCounter
			s.hasLast = true
		}
		return IssueDiscontinuity
	}
	if !p.HasPayload {
		return IssueNone
	}
	if s.hasLast {
		switch p.ContinuityCounter {
		case s.lastCC:
			s.stat.Duplicates++
			return IssueDuplicate
		case (s.lastCC + 1) & 0x0f:
			// In sequence.
		default:
			s.stat.ContinuityErrs++
			s.stat.LostPackets += int64((p.ContinuityCounter - s.lastCC - 1) & 0x0f)
			s.lastCC = p.ContinuityCounter
			return IssueCCJump
		}
	}
	s.lastCC = p.ContinuityCounter
	s.hasLast = true
	return IssueNone
}

// PIDStats returns a PID-number-sorted copy of all counters.
func (c *Counter) PIDStats() []PIDStat {
	out := make([]PIDStat, 0, len(c.pids))
	for _, s := range c.pids {
		out = append(out, s.stat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// PID returns the counters for one PID.
func (c *Counter) PID(pid uint16) (PIDStat, bool) {
	s, ok := c.pids[pid]
	if !ok {
		return PIDStat{}, false
	}
	return s.stat, true
}

// Totals aggregates the anomaly counters across all PIDs.
func (c *Counter) Totals() PIDStat {
	var t PIDStat
	for _, s := range c.pids {
		t.Packets += s.stat.Packets
		t.PayloadPackets += s.stat.PayloadPackets
		t.AdaptationOnly += s.stat.AdaptationOnly
		t.ContinuityErrs += s.stat.ContinuityErrs
		t.Duplicates += s.stat.Duplicates
		t.Discontinuities += s.stat.Discontinuities
		t.TransportErrors += s.stat.TransportErrors
		t.LostPackets += s.stat.LostPackets
	}
	return t
}
