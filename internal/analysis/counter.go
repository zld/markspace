// Package analysis assigns received TS packets to the effective program
// snapshot and accumulates per-stream, per-interval counts.
package analysis

import "tsaudit/internal/program"

// StreamCount accumulates packets of one elementary stream.
type StreamCount struct {
	PID        uint16
	StreamType uint8
	TypeName   string
	Packets    int
}

// ProgramCount accumulates the streams of one program in an interval.
type ProgramCount struct {
	Program    uint16
	PMTPID     uint16
	PMTVersion int
	Streams    []*StreamCount
}

// Interval is the span of received packets governed by one effective
// snapshot. Packets are counted as actually received; lost packets are
// never extrapolated into the counts.
type Interval struct {
	PATVersion  int
	StartPacket int
	EndPacket   int
	Programs    []*ProgramCount
	packets     int
}

// Packets returns the packets attributed to this interval.
func (i *Interval) Packets() int { return i.packets }

// Counter attributes packets to the current snapshot's streams.
type Counter struct {
	Intervals       []*Interval
	current         *Interval
	lookup          map[uint16]*StreamCount
	UnmappedPackets int
	UnmappedByPID   map[uint16]int
}

// NewCounter builds an empty counter.
func NewCounter() *Counter {
	return &Counter{UnmappedByPID: map[uint16]int{}}
}

// Rotate closes the current interval at startPacket-1 and opens a new
// one governed by snap, effective from startPacket on.
func (c *Counter) Rotate(snap *program.Snapshot, startPacket int) {
	if c.current != nil {
		c.current.EndPacket = startPacket - 1
	}
	iv := &Interval{PATVersion: snap.PATVersion, StartPacket: startPacket}
	c.lookup = map[uint16]*StreamCount{}
	for _, ps := range snap.Programs {
		pc := &ProgramCount{Program: ps.Program, PMTPID: ps.PMTPID, PMTVersion: ps.PMTVersion}
		for _, st := range ps.Streams {
			sc := &StreamCount{
				PID:        st.PID,
				StreamType: st.StreamType,
				TypeName:   program.StreamTypeName(st.StreamType),
			}
			pc.Streams = append(pc.Streams, sc)
			c.lookup[st.PID] = sc
		}
		iv.Programs = append(iv.Programs, pc)
	}
	c.Intervals = append(c.Intervals, iv)
	c.current = iv
}

// Count attributes one received packet. PIDs not mapped by the current
// snapshot are recorded as unmapped; they are never silently merged
// into a program.
func (c *Counter) Count(pid uint16) {
	if c.current != nil {
		if sc, ok := c.lookup[pid]; ok {
			sc.Packets++
			c.current.packets++
			return
		}
	}
	c.UnmappedPackets++
	c.UnmappedByPID[pid]++
}

// Close ends the final interval at lastPacket.
func (c *Counter) Close(lastPacket int) {
	if c.current != nil {
		c.current.EndPacket = lastPacket
	}
}
