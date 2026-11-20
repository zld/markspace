package psi

import "fmt"

// ElementaryStream is one PMT entry.
type ElementaryStream struct {
	StreamType uint8
	PID        uint16
}

// PMT is a validated Program Map Table.
type PMT struct {
	ProgramNumber uint16
	Version       uint8
	PCRPID        uint16
	Streams       []ElementaryStream
}

// ParsePMT decodes a PMT section body (program_info_length is honoured).
func ParsePMT(s Section) (*PMT, error) {
	if s.TableID != TableIDPMT {
		return nil, fmt.Errorf("psi: table 0x%02x is not a PMT", s.TableID)
	}
	if len(s.Body) < 4 {
		return nil, fmt.Errorf("%w: PMT body length %d too short", ErrInvalidSection, len(s.Body))
	}
	p := &PMT{
		ProgramNumber: s.Extension,
		Version:       s.Version,
		PCRPID:        uint16(s.Body[0]&0x1f)<<8 | uint16(s.Body[1]),
	}
	infoLen := int(s.Body[2]&0x0f)<<8 | int(s.Body[3])
	if 4+infoLen > len(s.Body) {
		return nil, fmt.Errorf("%w: PMT program_info_length %d overruns body %d", ErrInvalidSection, infoLen, len(s.Body))
	}
	rest := s.Body[4+infoLen:]
	for len(rest) > 0 {
		if len(rest) < 5 {
			return nil, fmt.Errorf("%w: truncated elementary stream entry", ErrInvalidSection)
		}
		es := ElementaryStream{
			StreamType: rest[0],
			PID:        uint16(rest[1]&0x1f)<<8 | uint16(rest[2]),
		}
		esInfo := int(rest[3]&0x0f)<<8 | int(rest[4])
		if 5+esInfo > len(rest) {
			return nil, fmt.Errorf("%w: PMT es_info_length %d overruns remaining %d", ErrInvalidSection, esInfo, len(rest))
		}
		p.Streams = append(p.Streams, es)
		rest = rest[5+esInfo:]
	}
	return p, nil
}
