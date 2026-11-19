package psi

import (
	"fmt"
)

// ProgramEntry maps one program_number to the PID carrying its PMT.
type ProgramEntry struct {
	ProgramNumber uint16
	PID           uint16
}

// PAT is one parsed PAT section. Programs excludes the network PID entry
// (program_number 0), which is reported separately.
type PAT struct {
	TransportStreamID uint16
	Version           uint8
	CurrentNext       bool
	SectionNumber     uint8
	LastSectionNumber uint8
	NetworkPID        uint16
	HasNetworkPID     bool
	Programs          []ProgramEntry
}

// ParsePAT parses the body of a PAT section produced by ParseSection.
func ParsePAT(s *Section) (*PAT, error) {
	if s.TableID != TableIDPAT {
		return nil, fmt.Errorf("psi: table 0x%02x is not PAT", s.TableID)
	}
	p := &PAT{
		TransportStreamID: s.Extension,
		Version:           s.Version,
		CurrentNext:       s.CurrentNext,
		SectionNumber:     s.SectionNumber,
		LastSectionNumber: s.LastSectionNumber,
	}
	body := s.Body
	for len(body) >= 4 {
		number := uint16(body[0])<<8 | uint16(body[1])
		pid := uint16(body[2]&0x1f)<<8 | uint16(body[3])
		body = body[4:]
		if number == 0 {
			p.NetworkPID = pid
			p.HasNetworkPID = true
			continue
		}
		for _, existing := range p.Programs {
			if existing.ProgramNumber == number {
				if existing.PID != pid {
					return nil, fmt.Errorf("psi: PAT program %d maps to both PID %d and %d", number, existing.PID, pid)
				}
				goto next
			}
		}
		p.Programs = append(p.Programs, ProgramEntry{ProgramNumber: number, PID: pid})
	next:
	}
	if len(body) != 0 {
		return nil, fmt.Errorf("%w: PAT has %d trailing bytes", ErrSectionLength, len(body))
	}
	return p, nil
}
