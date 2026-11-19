// Package psi implements MPEG-TS PSI section reassembly and PAT/PMT parsing.
// It deliberately does not use any PSI helper from go-astits: only the TS
// packet layer comes from that module.
package psi

import (
	"errors"
	"fmt"
)

const (
	// TableIDPAT is the program association table table_id.
	TableIDPAT = 0x00
	// TableIDPMT is the program map table table_id.
	TableIDPMT = 0x02

	// MaxSectionLength is the largest section_length accepted by the
	// reassembler. It bounds per-PID buffering on hostile input.
	MaxSectionLength = 4093

	sectionHeaderSize = 3
	sectionSyntaxSize = 5
	sectionCRCSize    = 4
)

var (
	// ErrSectionLength means a section declared an impossible length.
	ErrSectionLength = errors.New("psi: invalid section length")
	// ErrSectionCRC means a section failed its MPEG-2 CRC32 check.
	ErrSectionCRC = errors.New("psi: section CRC mismatch")
	// ErrSectionTruncated means a section ended before its declared length.
	ErrSectionTruncated = errors.New("psi: truncated section")
	// ErrSectionSyntax means PAT/PMT was not built with the syntax header.
	ErrSectionSyntax = errors.New("psi: section lacks syntax header")
)

// Section is one complete, length-validated PSI section. Body excludes the
// three byte header (plus syntax header) and the trailing CRC32.
type Section struct {
	TableID           uint8
	Extension         uint16
	Version           uint8
	CurrentNext       bool
	SectionNumber     uint8
	LastSectionNumber uint8
	Body              []byte
}

// CRC32 computes the MPEG-2 CRC32 used by PSI sections (polynomial
// 0x04C11DB7, initial value 0xFFFFFFFF, no final XOR).
func CRC32(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func validCRC(full []byte) bool { return CRC32(full) == 0 }

// ParseSection validates a complete section and decodes its syntax header.
func ParseSection(full []byte) (*Section, error) {
	if len(full) < sectionHeaderSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrSectionLength, len(full))
	}
	sectionLength := int(full[1]&0x0f)<<8 | int(full[2])
	if len(full) != sectionHeaderSize+sectionLength {
		return nil, fmt.Errorf("%w: declared %d, got %d", ErrSectionLength, sectionHeaderSize+sectionLength, len(full))
	}
	s := &Section{TableID: full[0]}
	syntax := full[1]&0x80 != 0
	if !syntax {
		if full[0] == TableIDPAT || full[0] == TableIDPMT {
			return nil, ErrSectionSyntax
		}
		s.Body = full[sectionHeaderSize:]
		return s, nil
	}
	if sectionLength < sectionSyntaxSize+sectionCRCSize+1 {
		return nil, fmt.Errorf("%w: section_length %d too small", ErrSectionLength, sectionLength)
	}
	s.Extension = uint16(full[3])<<8 | uint16(full[4])
	s.Version = (full[5] >> 1) & 0x1f
	s.CurrentNext = full[5]&0x01 != 0
	s.SectionNumber = full[6]
	s.LastSectionNumber = full[7]
	if s.SectionNumber > s.LastSectionNumber {
		return nil, fmt.Errorf("%w: section_number %d > last_section_number %d", ErrSectionLength, s.SectionNumber, s.LastSectionNumber)
	}
	if full[0] == TableIDPAT || full[0] == TableIDPMT {
		if !validCRC(full) {
			return nil, fmt.Errorf("%w: table 0x%02x", ErrSectionCRC, full[0])
		}
		s.Body = full[sectionSyntaxSize+sectionHeaderSize : len(full)-sectionCRCSize]
	}
	return s, nil
}

// Assembler reassembles sections for exactly one PID. It is not safe for
// concurrent use. A transport error or continuity anomaly must call Reset
// before the next payload is supplied.
