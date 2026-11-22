// Package segmenter selects the TS packets that belong to one program and
// writes them out in stream order. It reuses the pipeline engine, so a packet
// is selected exactly when its PID is part of the mapping interval effective
// at that packet index; when a remap moves a stream to another PID, the
// selection boundary moves with the mapping and the same PID can never be
// attributed to two different streams across a switch.
package segmenter

import (
	"errors"
	"io"
	"sort"

	"tsrebuild/internal/pipeline"
	"tsrebuild/internal/program"
)

// ErrProgramNotFound signals that the requested program never had a mapping.
type ErrProgramNotFound struct{ Program uint16 }

func (e ErrProgramNotFound) Error() string {
	return "segmenter: no mapping committed for program"
}

// PIDCount is one PID's emitted packet tally.
type PIDCount struct {
	PID     uint16
	Kind    string
	Packets int64
}

// IntervalReport counts packets emitted while one mapping was effective.
type IntervalReport struct {
	StartPacket  int64
	EndPacket    int64 // -1 while open
	PATVersion   uint8
	PMTVersion   uint8
	PMTPID       uint16
	Written      []PIDCount // PMT and elementary streams, sorted by PID
	totalWritten int64
}

// Report is the segmenter's deterministic accounting of what it wrote.
type Report struct {
	ProgramNumber  uint16
	InputPackets   int64
	WrittenPackets int64
	PATPackets     int64 // PAT (PID 0) is global and counted separately
	Intervals      []IntervalReport
}

// Selector decides packet membership for one program without doing any IO,
// which keeps the selection rule unit-testable.
type Selector struct {
	programNumber uint16
}

// NewSelector builds a selector for programNumber.
func NewSelector(programNumber uint16) *Selector {
	return &Selector{programNumber: programNumber}
}

// Wants reports whether ev's packet belongs to the selected program and must
// be emitted. PAT packets are global PSI and always wanted; every other PID
// has to be the program's PMT PID or one of its active elementary streams.
func (s *Selector) Wants(ev *pipeline.PacketEvent) bool {
	pkt := ev.Packet
	if pkt.PID == 0 {
		return true
	}
	if ev.Interval == nil {
		return false // no mapping in force yet: unattributable
	}
	return len(program.LookupPID(ev.Interval.Mapping, pkt.PID)) > 0
}

// Run streams r through the engine, writes every selected packet's raw 188
// bytes to w and returns the matching report. It never buffers the stream.
func Run(r io.Reader, w io.Writer, programNumber uint16) (*Report, error) {
	eng := pipeline.New(r, programNumber)
	sel := NewSelector(programNumber)
	rep := &Report{ProgramNumber: programNumber}

	var active *program.Interval
	var cur *IntervalReport
	counts := make(map[uint16]int64)
	kinds := make(map[uint16]string)
	// PAT packets seen before the selected program's first mapping are held
	// back so a stream with no such program produces no output at all. PAT is
	// low-rate and retransmitted periodically, so this buffer stays bounded.
	var pendingPAT [][]byte

	flush := func() {
		if cur == nil {
			return
		}
		cur.Written = sortedCounts(counts, kinds)
		cur.totalWritten = sumCounts(cur.Written)
		rep.Intervals = append(rep.Intervals, *cur)
	}

	for {
		ev, err := eng.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rep.InputPackets++

		if ev.Interval != nil && ev.Interval != active {
			flush()
			active = ev.Interval
			cur = newIntervalReport(active)
			// The previous mapping ended where this one becomes effective.
			if n := len(rep.Intervals); n > 0 {
				rep.Intervals[n-1].EndPacket = active.StartPacket
			}
			counts = make(map[uint16]int64)
			kinds = make(map[uint16]string)

			// First mapping for the selected program: release held PAT packets.
			if len(rep.Intervals) == 0 {
				for _, raw := range pendingPAT {
					if _, err := w.Write(raw); err != nil {
						return nil, err
					}
					rep.WrittenPackets++
					rep.PATPackets++
				}
				pendingPAT = nil
			}
		}

		if !sel.Wants(ev) {
			continue
		}
		if ev.Packet.PID == 0 && active == nil {
			pendingPAT = append(pendingPAT, ev.Packet.Raw)
			continue
		}
		if _, err := w.Write(ev.Packet.Raw); err != nil {
			return nil, err
		}
		rep.WrittenPackets++
		if ev.Packet.PID == 0 {
			rep.PATPackets++
			continue
		}
		counts[ev.Packet.PID]++
		kinds[ev.Packet.PID] = kindOf(ev)
	}
	flush()

	if len(rep.Intervals) == 0 {
		return nil, ErrProgramNotFound{Program: programNumber}
	}
	return rep, nil
}

func newIntervalReport(iv *program.Interval) *IntervalReport {
	return &IntervalReport{
		StartPacket: iv.StartPacket,
		EndPacket:   iv.EndPacket,
		PATVersion:  iv.Mapping.PATVersion,
		PMTVersion:  iv.Mapping.Version,
		PMTPID:      iv.Mapping.PMTPID,
	}
}

func kindOf(ev *pipeline.PacketEvent) string {
	for _, m := range program.LookupPID(ev.Interval.Mapping, ev.Packet.PID) {
		if m.IsPMT {
			return "psi"
		}
		if m.Stream != nil {
			return string(m.Stream.Kind)
		}
	}
	return "other"
}

func sortedCounts(counts map[uint16]int64, kinds map[uint16]string) []PIDCount {
	pids := make([]uint16, 0, len(counts))
	for pid := range counts {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	out := make([]PIDCount, 0, len(pids))
	for _, pid := range pids {
		out = append(out, PIDCount{PID: pid, Kind: kinds[pid], Packets: counts[pid]})
	}
	return out
}

func sumCounts(cs []PIDCount) int64 {
	var n int64
	for _, c := range cs {
		n += c.Packets
	}
	return n
}
