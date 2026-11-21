// Package psi implements a self-contained MPEG-TS PSI (Program Specific
// Information) parser: section reassembly, table-id-extension/version
// tracking, CRC-32/MPEG-2 validation and PAT/PMT interpretation.
package psi

import (
	"errors"
	"fmt"
)

// PSI table identifiers used by this tool.
const (
	TableIDPAT uint8 = 0x00 // Program Association Table
	TableIDPMT uint8 = 0x02 // Program Map Table
)

// maxSectionLength bounds the section_length field (12 bits, capped at 1021
// by ISO/IEC 13818-1 for PAT/PMT). Anything larger is a malformed section.
const maxSectionLength = 1021

// minSectionLength is the smallest legal PAT/PMT section: header (3) +
// table-syntax fields (5) + CRC (4).
const minSectionLength = 9

var (
	// ErrMalformedSection reports a structurally impossible section.
	ErrMalformedSection = errors.New("psi: malformed section")
	// ErrCRC reports a section whose CRC-32/MPEG-2 checksum does not match.
	ErrCRC = errors.New("psi: CRC-32/MPEG-2 mismatch")
	// ErrUnsupportedTable reports a table id this parser does not publish.
	ErrUnsupportedTable = errors.New("psi: unsupported table id")
)

// Section is one fully assembled PSI section before semantic interpretation.
type Section struct {
	TableID          uint8
	// SyntaxSection is true when the private/section-syntax-indicator bit is 1.
	SyntaxSection    bool
	// Length is the section_length value: bytes from after the length field to
	// the end of the section, including the CRC.
	Length           int
	TableIDExtension uint16 // transport_stream_id (PAT) or program_number (PMT)
	Version          uint8  // 5-bit table version
	CurrentNext      bool   // current_next_indicator
	SectionNumber    uint8
	LastSection      uint8
	// Raw holds the complete section including table_id and the CRC-32.
	Raw []byte
}

// ValidateSection checks section boundaries and length fields and verifies
// the CRC-32/MPEG-2 over the whole section. It returns a parsed Section
// descriptor only when every check passes.
func ValidateSection(data []byte) (*Section, error) {
	if len(data) < 3 {
		return nil, fmt.Errorf("%w: %d bytes, need at least 3", ErrMalformedSection, len(data))
	}
	tableID := data[0]
	if tableID != TableIDPAT && tableID != TableIDPMT {
		return nil, fmt.Errorf("%w: table_id 0x%02x", ErrUnsupportedTable, tableID)
	}

	syntax := data[1]&0x80 > 0
	if !syntax {
		return nil, fmt.Errorf("%w: section_syntax_indicator must be 1 for table_id 0x%02x",
			ErrMalformedSection, tableID)
	}
	// Reserved bits are the two bits 0x80..0x40 of the second byte; the low
	// nibble plus byte 3 form the 12-bit section_length.
	if data[1]&0xc0 != 0xc0 {
		return nil, fmt.Errorf("%w: reserved bits not set", ErrMalformedSection)
	}

	length := int(data[1]&0x0f)<<8 | int(data[2])
	if length < minSectionLength || length > maxSectionLength {
		return nil, fmt.Errorf("%w: section_length %d out of range [%d,%d]",
			ErrMalformedSection, length, minSectionLength, maxSectionLength)
	}
	// Total frame is the three leading bytes plus section_length bytes.
	if len(data) != 3+length {
		return nil, fmt.Errorf("%w: got %d bytes, section_length implies %d",
			ErrMalformedSection, len(data), 3+length)
	}

	// CRC-32/MPEG-2 covers the entire section and must equal zero when the
	// checksummed CRC field is included in the computation.
	if crc32MPEG2(data) != 0 {
		return nil, fmt.Errorf("%w: table_id 0x%02x extension 0x%04x",
			ErrCRC, tableID, uint16(data[3])<<8|uint16(data[4]))
	}

	return &Section{
		TableID:          tableID,
		SyntaxSection:    syntax,
		Length:           length,
		TableIDExtension: uint16(data[3])<<8 | uint16(data[4]),
		Version:          (data[5] >> 1) & 0x1f,
		CurrentNext:      data[5]&0x01 > 0,
		SectionNumber:    data[6],
		LastSection:      data[7],
		Raw:              append([]byte(nil), data...),
	}, nil
}

// SectionCandidate describes a damaged section attempt for diagnostic reports.
type SectionCandidate struct {
	PID     uint16 `json:"pid"`
	TableID uint8  `json:"table_id"`
	Version int    `json:"version"` // 5-bit candidate when decodable, -1 otherwise
	Reason  string `json:"reason"`
}

// crc32MPEG2 computes CRC-32/MPEG-2 (poly 0x04C11DB7, init 0xFFFFFFFF,
// no reflection, no final xor) using a table generated on package load.
func crc32MPEG2(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc = (crc << 8) ^ crcTableMPEG2[byte(crc>>24)^b]
	}
	return crc
}

// crcTableMPEG2 is the standard CRC-32/MPEG-2 lookup table.
var crcTableMPEG2 = func() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
		crc := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
		t[i] = crc
	}
	return t
}()
