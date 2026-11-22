// Package slicer attributes every TS packet to the mapping interval it
// belongs to and counts elementary-stream packets per interval. It also
// hosts the pipeline that wires tsreader, continuity tracking and PSI
// assembly together: tables produce versioned snapshots, the slicer
// assigns packets to snapshots and counts them.
package slicer

import (
	"io"

	"tsaudit/internal/continuity"
	"tsaudit/internal/model"
	"tsaudit/internal/psi"
	"tsaudit/internal/tsreader"
)

// NullPID carries padding; its continuity counter is meaningless.
const NullPID = 0x1FFF

// Anomaly counts continuity anomalies of one PID inside one interval.
type Anomaly struct {
	Gaps            int
	MissingPackets  int
	Duplicates      int
	TransportErrors int
}

// Interval holds the per-PID counters of one mapping interval.
type Interval struct {
	Snapshot  model.Snapshot
	Counts    map[uint16]int      // mapped elementary stream PID -> packets
	Unmapped  map[uint16]int      // non-PSI PID not in the mapping -> packets
	Anomalies map[uint16]*Anomaly // PID -> continuity anomalies
}

func newInterval(snap model.Snapshot) *Interval {
	return &Interval{
		Snapshot:  snap,
		Counts:    map[uint16]int{},
		Unmapped:  map[uint16]int{},
		Anomalies: map[uint16]*Anomaly{},
	}
}

// Result is the complete, recomputable outcome of one pipeline run.
type Result struct {
	TotalPackets     int
	PSIPackets       int
	Intervals        []*Interval // mapping intervals in order
	Pre              *Interval   // packets before the first snapshot
	PATChanges       int
	PMTChanges       int
	CorruptedSections int
	Incomplete       []psi.IncompleteUpdate
	Continuity       map[uint16]continuity.Stats
	HasTables        bool
}

type pipeline struct {
	asm        *psi.Assembler
	tracker    *continuity.Tracker
	reass      map[uint16]*psi.Reassembler
	intervals  []*Interval
	pre        *Interval
	psiPackets int
	total      int
}

// Run streams all packets of r through continuity tracking, PSI
// assembly and slicing, and returns the aggregated result.
func Run(r io.Reader) (*Result, error) {
	p := &pipeline{
		asm:     psi.NewAssembler(),
		tracker: continuity.NewTracker(),
		reass:   map[uint16]*psi.Reassembler{psi.PATPID: {}},
		pre:     newInterval(model.Snapshot{Index: -1}),
	}
	if err := tsreader.Read(r, p.observe); err != nil {
		return nil, err
	}
	pending := map[uint16]int{}
	for pid, re := range p.reass {
		pending[pid] = re.PendingMissing()
	}
	incomplete := p.asm.Incomplete(pending)
	// Sections that never completed a single section are invisible to
	// the assembler; flag them from the reassembly state instead.
	covered := map[uint16]bool{}
	for _, inc := range incomplete {
		covered[inc.PID] = true
	}
	for pid, re := range p.reass {
		if covered[pid] || !re.Pending() {
			continue
		}
		inc := psi.IncompleteUpdate{
			Table: "PSI", PID: pid, Version: -1,
			MissingPackets: (pending[pid] + 183) / 184,
		}
		if h, err := re.PendingHeader(); err == nil {
			inc.Version = h.Version
			inc.SectionsWant = h.LastSectionNumber + 1
			switch h.TableID {
			case psi.TableIDPAT:
				inc.Table = "PAT"
			case psi.TableIDPMT:
				inc.Table = "PMT"
			}
			if p.asm.IsCurrentVersion(pid, h.TableID, h.Version) {
				continue // truncated retransmission of the current table
			}
		}
		incomplete = append(incomplete, inc)
	}
	return &Result{
		TotalPackets:      p.total,
		PSIPackets:        p.psiPackets,
		Intervals:         p.intervals,
		Pre:               p.pre,
		PATChanges:        p.asm.PATChanges(),
		PMTChanges:        p.asm.PMTChanges(),
		CorruptedSections: p.asm.Corrupted(),
		Incomplete:        incomplete,
		Continuity:        p.tracker.Stats(),
		HasTables:         p.asm.HasTables(),
	}, nil
}

// observe processes one packet in input order: PSI assembly first (a
// completing table takes effect at this packet), then continuity, then
// slicing.
func (p *pipeline) observe(pkt tsreader.Packet) error {
	p.total++
	if re, ok := p.reass[pkt.PID]; ok {
		p.psiPackets++
		if pkt.HasPayload && !pkt.TransportError {
			re.Feed(pkt.PUSI, pkt.Payload, func(section []byte) {
				if snap, ok := p.asm.FeedSection(pkt.PID, section, pkt.Index); ok {
					p.intervals = append(p.intervals, newInterval(snap))
				}
			})
			p.syncReassemblers()
		}
	}
	if pkt.PID != NullPID {
		if ev := p.tracker.Observe(pkt.PID, pkt.CC, pkt.HasPayload, pkt.TransportError); ev.Kind != continuity.EventOK {
			p.recordAnomaly(pkt, ev)
		}
	}
	p.count(pkt)
	return nil
}

// syncReassemblers starts reassembly on PMT PIDs learned from the PAT.
func (p *pipeline) syncReassemblers() {
	for _, pid := range p.asm.PMTPIDs() {
		if _, ok := p.reass[pid]; !ok {
			p.reass[pid] = &psi.Reassembler{}
		}
	}
}

// intervalAt returns the interval in effect at the given packet index,
// or the pre-table bucket when no snapshot is effective yet.
func (p *pipeline) intervalAt(index int) *Interval {
	for i := len(p.intervals) - 1; i >= 0; i-- {
		if p.intervals[i].Snapshot.EffectivePacket <= index {
			return p.intervals[i]
		}
	}
	return p.pre
}

// count attributes a packet to its interval. PSI and null packets are
// not elementary-stream traffic and are excluded from the counts.
func (p *pipeline) count(pkt tsreader.Packet) {
	if pkt.PID == NullPID {
		return
	}
	if _, isPSI := p.reass[pkt.PID]; isPSI {
		return
	}
	iv := p.intervalAt(pkt.Index)
	if iv.Snapshot.PIDs[pkt.PID] {
		iv.Counts[pkt.PID]++
	} else {
		iv.Unmapped[pkt.PID]++
	}
}

// recordAnomaly attributes a continuity event to the packet's interval.
func (p *pipeline) recordAnomaly(pkt tsreader.Packet, ev continuity.Event) {
	iv := p.intervalAt(pkt.Index)
	a := iv.Anomalies[pkt.PID]
	if a == nil {
		a = &Anomaly{}
		iv.Anomalies[pkt.PID] = a
	}
	switch ev.Kind {
	case continuity.EventGap:
		a.Gaps++
		a.MissingPackets += ev.Missing
	case continuity.EventDuplicate:
		a.Duplicates++
	case continuity.EventTransportError:
		a.TransportErrors++
	}
}
