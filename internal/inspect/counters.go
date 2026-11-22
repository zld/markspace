// Package inspect maintains per-PID transport statistics. Continuity is
// tracked independently on every PID so that loss on one stream never clears
// PSI state or affects another PID, and packet counts are attributed to the
// program mapping interval effective when each packet arrived.
package inspect

import (
	"sort"

	"tsrebuild/internal/program"
	"tsrebuild/internal/psi"
	"tsrebuild/internal/tsreader"
)

// PIDStats holds the counters for one PID within one mapping interval.
type PIDStats struct {
	PID               uint16
	Kind              psi.StreamKind // "psi" for PAT/PMT PIDs, else ES kind
	IntervalStart     int64
	IntervalEnd       int64 // -1 while open
	Packets           int64
	PayloadPackets    int64
	ContinuityGaps    int64 // discontinuities of the 4-bit continuity counter
	DuplicateCounters int64 // same CC repeated on consecutive payload packets
	TransportErrors   int64 // packets with the transport error indicator set
}

// Collector accumulates statistics for one program plus the global PAT PID.
// It is driven in stream order and consults the timeline for attribution.
type Collector struct {
	programNumber uint16
	timeline      *program.Timeline

	current  *program.Interval
	stats    map[uint16]*PIDStats // stats for the active interval, keyed by PID
	perIV    []*PIDStats          // finalized stats, one entry per PID per interval
	patStats *PIDStats
	ccState  map[uint16]*ccState
}

type ccState struct {
	lastCC   uint8
	haveLast bool
}

// NewCollector creates a collector bound to one program and its timeline.
func NewCollector(programNumber uint16, tl *program.Timeline) *Collector {
	c := &Collector{
		programNumber: programNumber,
		timeline:      tl,
		stats:         make(map[uint16]*PIDStats),
		ccState:       make(map[uint16]*ccState),
	}
	c.patStats = &PIDStats{PID: tsreader.PIDPAT, Kind: "psi", IntervalStart: 0, IntervalEnd: -1}
	return c
}

// Observe feeds one packet in stream order. Packets of PIDs that do not
// belong to the selected program's current mapping are ignored except for
// continuity bookkeeping, which always stays per-PID.
func (c *Collector) Observe(pkt *tsreader.Packet) {
	// Continuity state is per PID, independent of mapping membership.
	state := c.ccState[pkt.PID]
	if state == nil {
		state = &ccState{}
		c.ccState[pkt.PID] = state
	}

	if pkt.PID == tsreader.PIDPAT {
		c.countPacket(c.patStats, pkt, state)
		return
	}

	// Refresh the effective mapping when the packet index crossed a boundary.
	if c.current == nil || !c.intervalContains(c.current, pkt.Index) {
		c.switchInterval(pkt.Index)
	}
	if c.current == nil {
		return // no mapping effective yet: unattributed packet
	}
	members := program.LookupPID(c.current.Mapping, pkt.PID)
	if len(members) == 0 {
		return // PID not part of this program's active mapping
	}

	stats := c.stats[pkt.PID]
	if stats == nil {
		stats = &PIDStats{
			PID:           pkt.PID,
			Kind:          c.kindOf(members),
			IntervalStart: c.current.StartPacket,
			IntervalEnd:   -1,
		}
		c.stats[pkt.PID] = stats
	}
	c.countPacket(stats, pkt, state)
}

// intervalContains reports whether index falls inside the half-open interval.
func (c *Collector) intervalContains(iv *program.Interval, index int64) bool {
	return index >= iv.StartPacket && (iv.EndPacket == -1 || index < iv.EndPacket)
}

// switchInterval finalizes the stats of the interval that just ended and
// prepares empty stats for whichever interval is now effective.
func (c *Collector) switchInterval(index int64) {
	iv, err := c.timeline.AtPacket(c.programNumber, index)
	if err != nil {
		c.current = nil
		return
	}
	if c.current == iv {
		c.current = iv
		return
	}
	for pid, s := range c.stats {
		s.IntervalEnd = iv.StartPacket
		c.perIV = append(c.perIV, s)
		delete(c.stats, pid)
	}
	c.current = iv
}

// kindOf derives the reported stream kind from the PID's mapping roles.
func (c *Collector) kindOf(members []program.PIDMember) psi.StreamKind {
	for _, m := range members {
		if m.IsPMT {
			return "psi"
		}
	}
	for _, m := range members {
		if m.Stream != nil {
			return m.Stream.Kind
		}
	}
	return psi.StreamOther
}

// countPacket updates one stats entry and advances the per-PID continuity
// state. TEI packets are counted as transport errors and never validate or
// invalidate the continuity counter (their payload is untrusted).
func (c *Collector) countPacket(s *PIDStats, pkt *tsreader.Packet, state *ccState) {
	s.Packets++
	if pkt.HasPayload {
		s.PayloadPackets++
	}
	if pkt.TransportError {
		s.TransportErrors++
		return
	}
	// A declared adaptation-field discontinuity resets continuity expectations.
	if pkt.AdaptationDiscontinuity {
		state.haveLast = false
	}
	if !pkt.HasPayload {
		// Adaptation-only packets carry no payload and do not increment CC.
		return
	}
	if state.haveLast {
		want := (state.lastCC + 1) & 0x0f
		if pkt.ContinuityCounter == state.lastCC {
			s.DuplicateCounters++
		} else if pkt.ContinuityCounter != want {
			s.ContinuityGaps++
		}
	}
	state.lastCC = pkt.ContinuityCounter
	state.haveLast = true
}

// Result is the finalized collector output.
type Result struct {
	ProgramNumber uint16
	PAT           PIDStats
	Intervals     [][]PIDStats // stats per mapping interval, in stream order
}

// Finalize closes the open interval and returns all stats ordered by packet
// index and PID, so the JSON output is deterministic.
func (c *Collector) Finalize() Result {
	for pid, s := range c.stats {
		c.perIV = append(c.perIV, s)
		delete(c.stats, pid)
	}
	grouped := make(map[int64][]PIDStats)
	var starts []int64
	for _, s := range c.perIV {
		if _, ok := grouped[s.IntervalStart]; !ok {
			starts = append(starts, s.IntervalStart)
		}
		grouped[s.IntervalStart] = append(grouped[s.IntervalStart], *s)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })

	out := Result{ProgramNumber: c.programNumber, PAT: *c.patStats}
	for _, start := range starts {
		group := grouped[start]
		sort.Slice(group, func(i, j int) bool { return group[i].PID < group[j].PID })
		out.Intervals = append(out.Intervals, group)
	}
	return out
}
