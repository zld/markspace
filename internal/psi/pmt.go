package psi

import (
	"fmt"
)

// ElementaryStream is one stream declaration inside a PMT.
type ElementaryStream struct {
	StreamType  uint8
	PID         uint16
	InfoLength  int
	Descriptors []byte
}

// PMT is one parsed PMT section. For a multi-section PMT the caller merges
// streams from every section of the same version.
type PMT struct {
	ProgramNumber     uint16
	Version           uint8
	CurrentNext       bool
	SectionNumber     uint8
	LastSectionNumber uint8
	PCRPID            uint16
	ProgramInfoLength int
	ProgramDescriptor []byte
	Streams           []ElementaryStream
}

// ParsePMT parses the body of a PMT section produced by ParseSection.
func ParsePMT(s *Section) (*PMT, error) {
	if s.TableID != TableIDPMT {
		return nil, fmt.Errorf("psi: table 0x%02x is not PMT", s.TableID)
	}
	if len(s.Body) < 4 {
		return nil, fmt.Errorf("%w: PMT body %d bytes", ErrSectionLength, len(s.Body))
	}
	p := &PMT{
		ProgramNumber:     s.Extension,
		Version:           s.Version,
		CurrentNext:       s.CurrentNext,
		SectionNumber:     s.SectionNumber,
		LastSectionNumber: s.LastSectionNumber,
		PCRPID:            uint16(s.Body[0]&0x1f)<<8 | uint16(s.Body[1]),
		ProgramInfoLength: int(s.Body[2]&0x0f)<<8 | int(s.Body[3]),
	}
	body := s.Body[4:]
	if p.ProgramInfoLength > len(body) {
		return nil, fmt.Errorf("%w: program_info_length %d > %d", ErrSectionLength, p.ProgramInfoLength, len(body))
	}
	p.ProgramDescriptor = append([]byte(nil), body[:p.ProgramInfoLength]...)
	body = body[p.ProgramInfoLength:]

	for len(body) > 0 {
		if len(body) < 5 {
			return nil, fmt.Errorf("%w: PMT ES header needs 5 bytes, have %d", ErrSectionLength, len(body))
		}
		streamType := body[0]
		pid := uint16(body[1]&0x1f)<<8 | uint16(body[2])
		infoLength := int(body[3]&0x0f)<<8 | int(body[4])
		body = body[5:]
		if infoLength > len(body) {
			return nil, fmt.Errorf("%w: ES_info_length %d > %d", ErrSectionLength, infoLength, len(body))
		}
		es := ElementaryStream{
			StreamType:  streamType,
			PID:         pid,
			InfoLength:  infoLength,
			Descriptors: append([]byte(nil), body[:infoLength]...),
		}
		for _, existing := range p.Streams {
			if existing.PID == pid {
				return nil, fmt.Errorf("psi: PMT program %d declares PID %d twice", p.ProgramNumber, pid)
			}
		}
		p.Streams = append(p.Streams, es)
		body = body[infoLength:]
	}
	return p, nil
}
