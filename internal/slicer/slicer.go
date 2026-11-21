// Package slicer extracts the transport packets belonging to one program. It
// reuses the same PSI assembler as the auditor so a packet is selected only
// when its PID is backed by a completely published, CRC-valid table.
package slicer

import (
	"errors"
	"io"

	"github.com/asticode/go-astits"

	"github.com/example/tsaudit/internal/program"
	"github.com/example/tsaudit/internal/psi"
	"github.com/example/tsaudit/internal/tsreader"
)

// ErrProgramNotFound reports that no complete PAT/PMT chain references the
// requested program.
var ErrProgramNotFound = errors.New("slicer: program not found in complete program table")

// MappingInterval marks one PMT mapping window observed inside the slice.
// A PID reused by a later PMT version yields one interval per version rather
// than being silently merged.
type MappingInterval struct {
	ElementaryPID uint16 `json:"elementary_pid"`
	StreamType    uint8  `json:"stream_type"`
	Version       uint8  `json:"version"`
	FirstPacket   int64  `json:"first_packet"`
	LastPacket    int64  `json:"last_packet"`
}

// Result describes a produced slice.
type Result struct {
	ProgramNumber    uint16                 `json:"program_number"`
	WrittenPackets   int64                  `json:"written_packets"`
	MappingIntervals []MappingInterval      `json:"mapping_intervals"`
	CorruptSections  []psi.SectionCandidate `json:"corrupt_sections"`
}

// Slicer selects packets for a single program while preserving input order.
type Slicer struct {
	programNumber uint16
	assembler     *psi.Assembler
	progs         *program.Map
	pmtPID        uint16
	haveProgram   bool
	written       int64
	// pendingPMTs are validated PMTs whose publication callback fired before
	// the PMT packet itself was appended to the slice; they are applied at the
	// correct slice-local packet index.
	pendingPMTs []*psi.PMT
}

// New creates a slicer for programNumber.
func New(programNumber uint16) *Slicer {
	s := &Slicer{
		programNumber: programNumber,
		assembler:     psi.NewAssembler(),
		progs:         program.New(),
	}
	s.assembler.OnPublication = s.onPublication
	return s
}

// Run reads every packet from r, writes the selected ones verbatim to w and
// returns slice statistics. Packet ordering is the input ordering.
func (s *Slicer) Run(r io.Reader, w io.Writer) (*Result, error) {
	reader := tsreader.NewReader(r)
	result := &Result{ProgramNumber: s.programNumber}

	for {
		pkt, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, tsreader.ErrTruncated) {
			return nil, tsreader.ErrTruncated
		}
		if errors.Is(err, tsreader.ErrSyncByte) {
			return nil, tsreader.ErrSyncByte
		}
		if err != nil {
			return nil, err
		}

		h := pkt.Header
		if h.TransportErrorIndicator || !pkt.SyncOK || !h.HasPayload {
			continue // damaged or payload-less packets are never part of a slice
		}

		// Re-run the identical PSI pipeline to confirm the mapping source.
		// Known elementary PIDs carry PES and never reach reassembly.
		if s.progs.IsElementaryPID(h.PID) {
			// not a PSI packet
		} else if h.PayloadUnitStartIndicator || h.PID == psi.PIDPAT ||
			(s.haveProgram && h.PID == s.pmtPID) {
			s.feedPSI(h, pkt.Payload)
		}

		if !s.shouldWrite(h.PID) {
			continue
		}
		// Apply any validated PMT published by this PMT packet before writing,
		// so the new mapping's interval starts at the PMT packet's index.
		s.applyPending()
		if _, err := w.Write(pkt.Raw[:]); err != nil {
			return nil, err
		}
		s.written++
		result.WrittenPackets++
	}

	if !s.haveProgram {
		return nil, ErrProgramNotFound
	}
	s.applyPending()
	s.progs.CloseIntervals(s.written - 1)
	result.MappingIntervals = s.intervals()
	result.CorruptSections = append([]psi.SectionCandidate(nil), s.assembler.Corrupt...)
	return result, nil
}

// feedPSI forwards a PSI payload to the shared section assembler. The
// tsreader has already removed the pointer_field, so payload[0] is the
// table id.
func (s *Slicer) feedPSI(h astits.PacketHeader, payload []byte) {
	if len(payload) == 0 {
		return
	}
	s.assembler.FeedPayload(h.PID, h.PayloadUnitStartIndicator, payload)
}

// shouldWrite keeps PAT packets, this program's PMT packets, and elementary
// packets currently mapped to the program. PAT packets are always retained:
// they are the table evidence backing the slice even before the first PMT
// version has been fully assembled.
func (s *Slicer) shouldWrite(pid uint16) bool {
	if pid == psi.PIDPAT {
		return true
	}
	if s.haveProgram && pid == s.pmtPID {
		return true
	}
	return s.progs.IsActive(s.programNumber, pid)
}

// onPublication mirrors the analyzer's table application path while deferring
// PMT index bookkeeping until the packet is actually written.
func (s *Slicer) onPublication(pub psi.Publication) {
	switch pub.TableID {
	case psi.TableIDPAT:
		s.progs.ApplyPAT(pub.PAT)
		if pmtPID, ok := pub.PAT.Programs[s.programNumber]; ok {
			s.pmtPID = pmtPID
		}
	case psi.TableIDPMT:
		if pub.PMT.ProgramNumber == s.programNumber {
			s.pendingPMTs = append(s.pendingPMTs, pub.PMT)
			s.haveProgram = true
		}
	}
}

// applyPending attaches queued PMTs at the current slice-local write index.
func (s *Slicer) applyPending() {
	for _, pmt := range s.pendingPMTs {
		s.progs.ApplyPMT(pmt, s.written)
	}
	s.pendingPMTs = nil
}

// intervals flattens this program's mapping records into slice metadata. The
// same PID under two PMT versions appears as two separate intervals.
func (s *Slicer) intervals() []MappingInterval {
	records := s.progs.Records()
	out := make([]MappingInterval, 0, len(records))
	for _, rec := range records {
		if rec.ProgramNumber != s.programNumber {
			continue
		}
		out = append(out, MappingInterval{
			ElementaryPID: rec.ElementaryPID,
			StreamType:    rec.StreamType,
			Version:       rec.FirstVersion,
			FirstPacket:   rec.FirstPacket,
			LastPacket:    rec.LastPacket,
		})
	}
	return out
}
