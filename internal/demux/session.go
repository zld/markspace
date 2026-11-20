package demux

import (
	"errors"
	"fmt"
	"io"

	"tsinspect/internal/psi"
	"tsinspect/internal/ts"
)

// PATPID is the fixed PID carrying program association tables.
const PATPID uint16 = 0x0000

// Session couples continuity accounting with PSI reassembly and table state.
// Both inspect and slice drive the same session, so slicing decisions are
// always derived from a CRC-validated, complete table version.
type Session struct {
	Tables  *psi.Tables
	Counter *Counter

	assemblers map[uint16]*psi.Assembler
	psiErrors  int
	sectionCRC int
	sections   int
}

// NewSession creates an empty parse session.
func NewSession() *Session {
	return &Session{
		Tables:     psi.NewTables(),
		Counter:    NewCounter(),
		assemblers: make(map[uint16]*psi.Assembler),
	}
}

func (s *Session) assembler(pid uint16) *psi.Assembler {
	a := s.assemblers[pid]
	if a == nil {
		a = psi.NewAssembler()
		s.assemblers[pid] = a
	}
	return a
}

// IsPSIPID reports whether pid currently carries PAT or PMT data.
func (s *Session) IsPSIPID(pid uint16) bool {
	if pid == PATPID {
		return true
	}
	for _, p := range s.Tables.PMTPIDs() {
		if p == pid {
			return true
		}
	}
	return false
}

// sniffsPMT allows a PMT to be reassembled before any PAT has been seen, so a
// stream starting mid-file still recovers its program map. It only inspects a
// payload-unit-start payload, and only while no PAT is active. Mappings are
// still validated against the PAT before they are exposed.
func (s *Session) sniffsPMT(p *ts.Packet) bool {
	if s.Tables.PAT() != nil || !p.PayloadUnitStart || len(p.Payload) < 3 {
		return false
	}
	pointer := int(p.Payload[0])
	if 1+pointer >= len(p.Payload) {
		return false
	}
	return p.Payload[1+pointer] == psi.TableIDPMT
}

// Feed consumes one packet. Recoverable corruption (bad CRC, impossible
// section length, continuity jump) is accounted for and yields a nil error;
// only structural failures surface to the caller.
func (s *Session) Feed(p *ts.Packet) error {
	issue := s.Counter.Observe(p)
	switch issue {
	case IssueTransportError:
		// A corrupt packet tells us nothing about lost neighbours, so the
		// continuity baseline is dropped without discarding PSI state.
		if a := s.assemblers[p.PID]; a != nil {
			a.Discard(psi.DiscardTransport)
		}
	case IssueCCJump:
		// Packets were lost: any partially assembled section on this PID is
		// unusable, and so are pending tables fed by it.
		if a := s.assemblers[p.PID]; a != nil {
			a.Discard(psi.DiscardContinuity)
		}
		s.Tables.DiscardPendingPID(p.PID)
	}
	if p.TransportError {
		// The payload of a packet flagged corrupt cannot be trusted, even
		// though later packets on the PID may be fine.
		return nil
	}
	if !p.HasPayload || len(p.Payload) == 0 {
		return nil
	}
	if !s.IsPSIPID(p.PID) && !s.sniffsPMT(p) {
		return nil
	}
	sections, err := s.assembler(p.PID).Push(p.Payload, p.PayloadUnitStart)
	for _, sec := range sections {
		s.sections++
		s.dispatch(p.PID, sec)
	}
	if err != nil && !errors.Is(err, psi.ErrCRCMismatch) && !errors.Is(err, psi.ErrInvalidSection) {
		return fmt.Errorf("demux: reassembling PSI on PID %d: %w", p.PID, err)
	}
	return nil
}

func (s *Session) dispatch(pid uint16, sec psi.Section) {
	var err error
	switch sec.TableID {
	case psi.TableIDPAT:
		_, err = s.Tables.AddPAT(sec)
	case psi.TableIDPMT:
		_, err = s.Tables.AddPMT(pid, sec)
	}
	if err != nil && !errors.Is(err, psi.ErrInvalidSection) {
		s.psiErrors++
	}
}

// SectionStats summarises reassembly activity across all PIDs.
type SectionStats struct {
	Sections     int
	CRCErrors    int
	LengthErrors int
	Discarded    int
}

// SectionStats aggregates the per-PID assembler counters.
func (s *Session) SectionStats() SectionStats {
	var out SectionStats
	out.Sections = s.sections
	for _, a := range s.assemblers {
		st := a.Stats()
		out.CRCErrors += st.CRCErrors
		out.LengthErrors += st.LengthErrors
		out.Discarded += st.Discarded
	}
	return out
}

// ParseErrorCount returns non-fatal table/PSI semantic errors seen so far.
func (s *Session) ParseErrorCount() int { return s.psiErrors }

// ErrNoPackets indicates the input contained no synchronized TS packet.
var ErrNoPackets = errors.New("input contains no transport packets")

// ParseFile streams every packet of path through a fresh session.
func ParseFile(path string, offset int64) (*Session, *ts.Reader, error) {
	r, err := ts.Open(path, offset)
	if err != nil {
		return nil, nil, err
	}
	s := NewSession()
	for {
		p, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			r.Close()
			return nil, nil, err
		}
		if err := s.Feed(p); err != nil {
			r.Close()
			return nil, nil, err
		}
	}
	if r.Packets() == 0 {
		r.Close()
		return nil, nil, fmt.Errorf("%w: no %d-byte packet with sync byte 0x47 was found", ErrNoPackets, ts.PacketSize)
	}
	return s, r, nil
}
