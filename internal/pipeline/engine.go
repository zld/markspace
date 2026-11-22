// Package pipeline wires the streaming stages together: TS reader, per-PID
// PSI assembly, PAT/PMT validation and the program mapping timeline. The same
// engine drives both the inspection report and the segmenter so they can
// never disagree about which mapping is in force for a packet.
package pipeline

import (
	"io"

	"tsrebuild/internal/inspect"
	"tsrebuild/internal/program"
	"tsrebuild/internal/psi"
	"tsrebuild/internal/tsreader"
)

// PacketEvent is one processed packet plus the mapping context in force for
// the selected program when it arrived.
type PacketEvent struct {
	Packet         *tsreader.Packet
	Interval       *program.Interval // nil until the program's first mapping commits
	MappingChanged bool              // true when a new interval became effective at this packet
}

// Engine processes one TS stream for one selected program.
type Engine struct {
	reader        *tsreader.Reader
	asm           *psi.Assembler
	patTracker    *psi.PATTracker
	pmtTracker    *psi.PMTTracker
	collector     *inspect.Collector
	programNumber uint16

	timeline *program.Timeline // created when the first PAT commits
	psiPIDs  map[uint16]bool   // PAT PID plus every PMT PID seen in a committed PAT
	current  *program.Interval // active interval for the selected program
}

// New creates an engine reading from r for the given program number. The
// timeline is created up front (its TSID is filled in by the first PAT) so
// that global PAT statistics are captured from the very first packet.
func New(r io.Reader, programNumber uint16) *Engine {
	tl := program.NewTimeline(0)
	return &Engine{
		reader:        tsreader.NewReader(r),
		asm:           psi.NewAssembler(),
		patTracker:    psi.NewPATTracker(),
		pmtTracker:    psi.NewPMTTracker(),
		programNumber: programNumber,
		psiPIDs:       map[uint16]bool{tsreader.PIDPAT: true},
		timeline:      tl,
		collector:     inspect.NewCollector(programNumber, tl),
	}
}

// timelineSeen reports whether at least one PAT has committed.
func (e *Engine) timelineSeen() bool { return e.patTracker.Current() != nil }

// Next reads and processes one packet. io.EOF marks a clean end of stream.
func (e *Engine) Next() (*PacketEvent, error) {
	pkt, err := e.reader.Next()
	if err != nil {
		return nil, err
	}
	changed := false
	if e.psiPIDs[pkt.PID] {
		sections, aerr := e.asm.Push(pkt)
		if aerr != nil {
			return nil, aerr
		}
		changed, err = e.handleSections(sections)
		if err != nil {
			return nil, err
		}
	}

	// Track the selected program's active interval.
	if e.timeline != nil && (e.current == nil || !contains(e.current, pkt.Index)) {
		if iv, lerr := e.timeline.AtPacket(e.programNumber, pkt.Index); lerr == nil && iv != e.current {
			e.current = iv
			changed = true
		}
	}
	if e.collector != nil {
		e.collector.Observe(pkt)
	}
	return &PacketEvent{Packet: pkt, Interval: e.current, MappingChanged: changed}, nil
}

// handleSections feeds assembled sections to the trackers and commits any new
// mapping to the timeline, registering newly discovered PMT PIDs.
func (e *Engine) handleSections(sections []*psi.Section) (bool, error) {
	changed := false
	for _, sec := range sections {
		switch sec.PID {
		case tsreader.PIDPAT:
			pat, _, err := e.patTracker.Push(sec)
			if err != nil || pat == nil {
				continue // bad CRC / incomplete: previous mapping stays active
			}
			e.timeline.ApplyPAT(pat)
			for _, p := range pat.Programs {
				e.psiPIDs[p.PMTPID] = true
			}
		default:
			pmt, _, err := e.pmtTracker.Push(sec)
			if err != nil || pmt == nil {
				continue
			}
			programNumber, ok := e.programForPMTPID(sec.PID)
			if !ok {
				continue // PMT PID not referenced by any committed PAT
			}
			patVersion := uint8(0)
			if cur := e.patTracker.Current(); cur != nil {
				patVersion = cur.Version
			}
			iv, merr := e.timeline.ApplyMapping(programNumber, sec.PID, patVersion,
				pmt.Version, pmt, sec.StartPacket)
			if merr != nil {
				return false, merr
			}
			if programNumber == e.programNumber && iv != e.current {
				changed = true
			}
		}
	}
	return changed, nil
}

// programForPMTPID resolves the program number owning a PMT PID via the last
// committed PAT.
func (e *Engine) programForPMTPID(pmtPID uint16) (uint16, bool) {
	cur := e.patTracker.Current()
	if cur == nil {
		return 0, false
	}
	for _, p := range cur.Programs {
		if p.PMTPID == pmtPID {
			return p.ProgramNumber, true
		}
	}
	return 0, false
}

func contains(iv *program.Interval, index int64) bool {
	return index >= iv.StartPacket && (iv.EndPacket == -1 || index < iv.EndPacket)
}

// Timeline returns the mapping timeline (nil before the first PAT commits).
func (e *Engine) Timeline() *program.Timeline { return e.timeline }

// Stats finalizes and returns the per-PID statistics.
func (e *Engine) Stats() inspect.Result {
	if e.collector == nil {
		return inspect.Result{ProgramNumber: e.programNumber}
	}
	return e.collector.Finalize()
}

// RejectedCRC returns the number of PAT/PMT sections dropped for bad CRC.
func (e *Engine) RejectedCRC() (pat int, pmt int) {
	return e.patTracker.RejectedCRC, e.pmtTracker.RejectedCRC
}

// AssemblerStats exposes PSI assembly diagnostics for a PID.
func (e *Engine) AssemblerStats(pid uint16) psi.Stats { return e.asm.Stats(pid) }

// PacketCount returns the number of packets consumed.
func (e *Engine) PacketCount() int64 { return e.reader.Index() }
