package psi

import (
	"fmt"
)

// parsePAT interprets the completed sections of a Program Association Table.
// Sections are visited in section_number order; program 0 is the network PID
// and is skipped, every other program maps to its PMT PID.
func parsePAT(sections map[uint8]*Section) (*PAT, error) {
	ordered, err := orderSections(sections)
	if err != nil {
		return nil, err
	}
	pat := &PAT{Programs: map[uint16]uint16{}}
	version := ordered[0].Version
	for _, s := range ordered {
		if s.Version != version {
			return nil, fmt.Errorf("mixed versions %d/%d inside one PAT", version, s.Version)
		}
		// PAT loop starts after table-id(1)+header fields(7) and ends before
		// the 4-byte CRC.
		body := s.Raw[8 : len(s.Raw)-4]
		if len(body)%4 != 0 {
			return nil, fmt.Errorf("PAT loop length %d is not a multiple of 4", len(body))
		}
		for i := 0; i+4 <= len(body); i += 4 {
			program := uint16(body[i])<<8 | uint16(body[i+1])
			pid := uint16(body[i+2]&0x1f)<<8 | uint16(body[i+3])
			if program == 0 {
				continue // network_PID, not a program
			}
			pat.Programs[program] = pid
		}
	}
	pat.Version = version
	return pat, nil
}

// parsePMT interprets the completed sections of a Program Map Table. The
// single-section form mandated for PMTs is enforced here.
func parsePMT(sections map[uint8]*Section) (*PMT, error) {
	ordered, err := orderSections(sections)
	if err != nil {
		return nil, err
	}
	if len(ordered) != 1 || ordered[0].LastSection != 0 {
		return nil, fmt.Errorf("PMT must be a single section, got %d sections", len(ordered))
	}
	s := ordered[0]
	raw := s.Raw
	// PMT fixed header through ES_info_length occupies bytes 8..12.
	if len(raw) < 13+4 {
		return nil, fmt.Errorf("PMT section too short: %d bytes", len(raw))
	}
	pmt := &PMT{
		ProgramNumber: s.TableIDExtension,
		Version:       s.Version,
		PCRPID:        uint16(raw[8]&0x1f)<<8 | uint16(raw[9]),
	}
	programInfoLen := int(raw[10]&0x0f)<<8 | int(raw[11])
	pos := 12 + programInfoLen
	end := len(raw) - 4 // exclude CRC
	if pos > end {
		return nil, fmt.Errorf("program_info_length %d exceeds PMT body", programInfoLen)
	}

	for pos+5 <= end {
		streamType := raw[pos]
		elementaryPID := uint16(raw[pos+1]&0x1f)<<8 | uint16(raw[pos+2])
		esInfoLen := int(raw[pos+3]&0x0f)<<8 | int(raw[pos+4])
		next := pos + 5 + esInfoLen
		if next > end {
			return nil, fmt.Errorf("ES_info_length %d exceeds PMT body", esInfoLen)
		}
		// Unknown stream types are retained raw; no descriptor decoding is
		// required by this tool.
		pmt.Streams = append(pmt.Streams, StreamInfo{
			StreamType:    streamType,
			ElementaryPID: elementaryPID,
		})
		pos = next
	}
	if pos != end {
		return nil, fmt.Errorf("PMT loop ended %d bytes before CRC", end-pos)
	}
	return pmt, nil
}

// orderSections returns sections sorted by section_number and verifies that
// they form one contiguous 0..last_section_number set.
func orderSections(sections map[uint8]*Section) ([]*Section, error) {
	if len(sections) == 0 {
		return nil, fmt.Errorf("no sections to interpret")
	}
	out := make([]*Section, 0, len(sections))
	for i := uint8(0); int(i) < len(sections); i++ {
		s, ok := sections[i]
		if !ok {
			return nil, fmt.Errorf("missing section_number %d", i)
		}
		if int(s.LastSection) != len(sections)-1 {
			return nil, fmt.Errorf("inconsistent last_section_number %d", s.LastSection)
		}
		out = append(out, s)
	}
	return out, nil
}

// CRC32MPEG2 exposes the checksum so the test-data generator can build
// spec-compliant sections without re-implementing the polynomial.
func CRC32MPEG2(data []byte) uint32 { return crc32MPEG2(data) }
