package demux

import (
	"fmt"
	"io"

	"tsinspect/internal/metrics"
	"tsinspect/internal/program"
	"tsinspect/internal/transport"
)

// Slicer writes only the selected program's PMT and elementary stream packets.
// It never parses PSI: relevance comes exclusively from the effective mapping
// owned by program.Manager, so there is a single source of truth.
type Slicer struct {
	w              io.Writer
	enabled        bool
	program        uint16
	maxBuffer      int
	stats          SliceStats
	pending        []*transport.Packet
	pmtPID         uint16
	streamPIDs     map[uint16]bool
	version        uint8
	haveTable      bool
	ended          bool
	curPMT         *program.PMTState
	boundaryOffset int64
	boundaryDue    *Boundary
	writeErr       error
}

// NewSlicer creates a slicer. A nil writer disables output but keeps stats.
func NewSlicer(w io.Writer, programNumber uint16, maxBuffer int, enabled bool) *Slicer {
	if maxBuffer <= 0 {
		maxBuffer = 4096
	}
	s := &Slicer{
		w:          w,
		enabled:    enabled && programNumber != 0,
		program:    programNumber,
		maxBuffer:  maxBuffer,
		streamPIDs: map[uint16]bool{},
	}
	s.stats.Enabled = s.enabled
	s.stats.Program = programNumber
	return s
}

// Observe consumes one packet after continuity classification. psiDamaged
// reports that the packet produced an invalid PSI section, so its payload must
// not be copied into a slice even though the TS framing was intact.
func (s *Slicer) Observe(pkt *transport.Packet, verdict metrics.Verdict, progs *program.Manager, psiDamaged bool) {
	if !s.enabled || s.writeErr != nil {
		return
	}
	if s.ended {
		// After the selected program disappears from the PAT the slice ends.
		// Only a later re-addition of the program can restart it.
		if progs.PMT(s.program) != nil {
			s.ended = false
		} else {
			return
		}
	}
	pmt := progs.PMT(s.program)
	if pmt == nil {
		if patExcludes(progs, s.program) {
			// The program is not in the effective PAT (removed, or never
			// present). Stop rather than mixing unrelated packets into the
			// slice; an empty output is safer than a wrong one.
			s.ended = true
			if s.haveTable {
				s.stats.ProgramEnded = true
			} else {
				s.stats.ProgramAbsent = true
			}
			s.dropPending()
			return
		}
		s.buffer(pkt)
		return
	}
	if !s.haveTable || pmt.Version != s.version || pmt.PID != s.pmtPID {
		// Cached packets predate the table but still belong to the program.
		// They are written first, so the boundary is anchored at the earliest
		// relevant one; without a cache the live packet is the anchor. The
		// output position is filled in when the first packet is written.
		s.boundaryOffset = pkt.Offset
		if p := firstRelevant(s.pending, pmt); p != nil {
			s.boundaryOffset = p.Offset
		}
		s.adoptTable(pmt)
		s.flushPending()
	}

	relevant := pkt.PID == s.pmtPID || s.streamPIDs[pkt.PID]
	if !relevant {
		return
	}
	// Damaged or duplicate packets are never written. A duplicate would
	// inflate the slice, and a transport-error packet is known-corrupt; after
	// a jump the payloads are intact, so they are copied as-is.
	if verdict == metrics.VerdictTransportError || verdict == metrics.VerdictUnjudgeable ||
		verdict == metrics.VerdictDuplicate || psiDamaged {
		return
	}
	s.emit(pkt)
}

func patExcludes(progs *program.Manager, programNumber uint16) bool {
	pat := progs.PAT()
	if pat == nil {
		return false
	}
	_, ok := pat.Programs[programNumber]
	return !ok
}

// adoptTable refreshes the PID mapping from the effective PMT.
func (s *Slicer) adoptTable(pmt *program.PMTState) {
	s.pmtPID = pmt.PID
	s.version = pmt.Version
	s.haveTable = true
	s.curPMT = pmt
	s.streamPIDs = make(map[uint16]bool, len(pmt.Streams))
	for _, es := range pmt.Streams {
		s.streamPIDs[es.PID] = true
	}
	s.stats.VersionSeen = pmt.Version
	s.stats.HaveVersion = true
	s.stageBoundary()
}

func (s *Slicer) emit(pkt *transport.Packet) {
	s.noteBoundaryIfDue()
	if s.w != nil {
		n, err := s.w.Write(pkt.Raw[:])
		if err != nil && s.writeErr == nil {
			s.writeErr = fmt.Errorf("demux: writing slice packet: %w", err)
			return
		}
		if n != transport.PacketSize && s.writeErr == nil {
			s.writeErr = fmt.Errorf("demux: short slice write: %d of %d bytes", n, transport.PacketSize)
			return
		}
	}
	s.stats.PacketsWritten++
	if pkt.PID == s.pmtPID {
		s.stats.PMTPackets++
	} else {
		s.stats.ESPackets++
	}
}

// Close reports a pending write failure and releases the cache.
func (s *Slicer) Close() error {
	s.pending = nil
	return s.writeErr
}
