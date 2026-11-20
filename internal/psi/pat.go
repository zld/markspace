package psi

import (
	"fmt"
)

// TableID values owned by this tool.
const (
	TableIDPAT = 0x00
	TableIDPMT = 0x02
)

// PATProgram maps a program number to the PID carrying its PMT.
type PATProgram struct {
	ProgramNumber uint16
	PMTPID        uint16
}

// PAT is a validated Program Association Table.
type PAT struct {
	TransportStreamID uint16
	Version           uint8
	Programs          []PATProgram
}

// PMTPID returns the PMT PID for program, if present.
func (p *PAT) PMTPID(program uint16) (uint16, bool) {
	if p == nil {
		return 0, false
	}
	for _, e := range p.Programs {
		if e.ProgramNumber == program {
			return e.PMTPID, true
		}
	}
	return 0, false
}

// ParsePAT decodes a PAT section body. Program number 0 (NIT) is skipped.
func ParsePAT(s Section) (*PAT, error) {
	if s.TableID != TableIDPAT {
		return nil, fmt.Errorf("psi: table 0x%02x is not a PAT", s.TableID)
	}
	if len(s.Body)%4 != 0 {
		return nil, fmt.Errorf("%w: PAT body length %d is not a multiple of 4", ErrInvalidSection, len(s.Body))
	}
	p := &PAT{TransportStreamID: s.Extension, Version: s.Version}
	for i := 0; i < len(s.Body); i += 4 {
		program := uint16(s.Body[i])<<8 | uint16(s.Body[i+1])
		pid := uint16(s.Body[i+2]&0x1f)<<8 | uint16(s.Body[i+3])
		if program == 0 {
			continue // network information table
		}
		p.Programs = append(p.Programs, PATProgram{ProgramNumber: program, PMTPID: pid})
	}
	return p, nil
}
