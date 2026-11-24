package ts

import (
	"encoding/binary"
	"fmt"

	"medpipe/internal/fault"
)

// MPEG-TS stream types supported by medpipe.
const (
	StreamTypeMPEG1Audio uint8 = 0x03
	StreamTypeAACADTS    uint8 = 0x0f
	StreamTypeH264       uint8 = 0x1b
	StreamTypeH265       uint8 = 0x24
)

// TrackDescription is the active PSI-derived elementary track map.
type TrackDescription struct {
	PID        uint16 `json:"pid"`
	StreamType uint8  `json:"stream_type"`
	Codec      string `json:"codec"`
	Kind       string `json:"kind"`
}

// PATProgram is one program entry in a PAT.
type PATProgram struct {
	Number uint16
	PMTPID uint16
}

// PAT is a committed Program Association Table.
type PAT struct {
	Version      uint8
	TransportID  uint16
	Programs     []PATProgram
	SectionCount int
}

// PMT is a committed Program Map Table.
type PMT struct {
	Version       uint8
	ProgramNumber uint16
	PCRPID        uint16
	PID           uint16
	Tracks        []TrackDescription
}

// Section is a validated MPEG-TS PSI section.
type Section struct {
	Raw             []byte
	TableID         uint8
	SyntaxIndicator bool
	Length          uint16
	Version         uint8
	CurrentNext     bool
	SectionNumber   uint8
	LastSection     uint8
	Payload         []byte
	CRC             uint32
	CalculatedCRC   uint32
	CRCOK           bool
}

// CRC32MPEG computes the non-reflected MPEG-2 PSI CRC32:
// polynomial 0x04C11DB7, initial value 0xFFFFFFFF, no final XOR.
func CRC32MPEG(data []byte) uint32 {
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

// ParseSection parses and validates one complete PSI section. The returned
// section is non-nil even when CRC validation fails so callers can report the
// version and location without committing it.
func ParseSection(raw []byte) (*Section, *fault.Error) {
	if len(raw) < 3 {
		return nil, fault.New(fault.ErrPSISection, "psi.section", 0, "section shorter than 3 bytes")
	}
	length := uint16(raw[1]&0x0f)<<8 | uint16(raw[2])
	total := int(length) + 3
	if length < 9 || total > len(raw) {
		return nil, fault.New(fault.ErrPSISection, "psi.section", 0,
			fmt.Sprintf("declared section length %d needs %d bytes, have %d", length, total, len(raw)))
	}
	if total < 9 {
		return nil, fault.New(fault.ErrPSISection, "psi.section", 0, "section too short for header and CRC")
	}
	s := &Section{
		Raw:             append([]byte(nil), raw[:total]...),
		TableID:         raw[0],
		SyntaxIndicator: raw[1]&0x80 != 0,
		Length:          length,
		Version:         (raw[5] >> 1) & 0x1f,
		CurrentNext:     raw[5]&0x01 == 1,
		SectionNumber:   raw[6],
		LastSection:     raw[7],
		Payload:         raw[8 : total-4],
		CRC:             binary.BigEndian.Uint32(raw[total-4 : total]),
	}
	s.CalculatedCRC = CRC32MPEG(raw[:total-4])
	s.CRCOK = s.CRC == s.CalculatedCRC
	if !s.CRCOK {
		return s, fault.New(fault.ErrBadCRC, "psi.crc", 0,
			fmt.Sprintf("table=0x%02x version=%d crc=%08x calculated=%08x",
				s.TableID, s.Version, s.CRC, s.CalculatedCRC))
	}
	if s.SectionNumber > s.LastSection {
		return s, fault.New(fault.ErrPSISection, "psi.section", 0,
			fmt.Sprintf("section_number %d > last_section_number %d", s.SectionNumber, s.LastSection))
	}
	return s, nil
}

// CodecForStreamType maps a TS stream type to a supported output sample entry.
// Unknown types return ok=false so the demux can reject rather than invent a
// codec mapping.
func CodecForStreamType(streamType uint8) (codec, kind string, ok bool) {
	switch streamType {
	case StreamTypeH264:
		return "avc1", "video", true
	case StreamTypeH265:
		return "hvc1", "video", true
	case StreamTypeAACADTS:
		return "mp4a", "audio", true
	case StreamTypeMPEG1Audio:
		return "mp4a", "audio", true
	default:
		return "", "", false
	}
}

// ParsePATPayload parses program entries from a complete PAT section set.
func ParsePATPayload(sections []*Section) (*PAT, *fault.Error) {
	if len(sections) == 0 {
		return nil, fault.New(fault.ErrPSISection, "psi.pat", 0, "no PAT sections")
	}
	pat := &PAT{Version: sections[0].Version, SectionCount: len(sections)}
	for _, s := range sections {
		if len(s.Raw) < 8 {
			return nil, fault.New(fault.ErrPSISection, "psi.pat", 0, "short PAT header")
		}
		pat.TransportID = binary.BigEndian.Uint16(s.Raw[3:5])
		// Payload starts after section_number/last_section_number (offset 8).
		for off := 8; off+4 <= len(s.Raw)-4; off += 4 {
			num := binary.BigEndian.Uint16(s.Raw[off : off+2])
			pid := binary.BigEndian.Uint16(s.Raw[off+2:off+4]) & 0x1fff
			if num == 0 {
				continue // NIT PID
			}
			pat.Programs = append(pat.Programs, PATProgram{Number: num, PMTPID: pid})
		}
	}
	return pat, nil
}

// ParsePMTPayload parses elementary stream entries from a PMT section.
func ParsePMTPayload(pid uint16, s *Section) (*PMT, *fault.Error) {
	if s == nil || len(s.Raw) < 12 {
		return nil, fault.New(fault.ErrPSISection, "psi.pmt", 0, "short PMT section")
	}
	pmt := &PMT{
		PID:           pid,
		Version:       s.Version,
		ProgramNumber: binary.BigEndian.Uint16(s.Raw[3:5]),
		PCRPID:        binary.BigEndian.Uint16(s.Raw[8:10]) & 0x1fff,
	}
	pil := binary.BigEndian.Uint16(s.Raw[10:12]) & 0x0fff
	pos := 12 + int(pil)
	end := len(s.Raw) - 4 // exclude CRC
	for pos+5 <= end {
		st := s.Raw[pos]
		epid := binary.BigEndian.Uint16(s.Raw[pos+1:pos+3]) & 0x1fff
		esInfoLen := binary.BigEndian.Uint16(s.Raw[pos+3:pos+5]) & 0x0fff
		pos += 5
		if pos+int(esInfoLen) > end {
			return nil, fault.New(fault.ErrPSISection, "psi.pmt", int64(pos), "PMT ES descriptors exceed section")
		}
		pos += int(esInfoLen)
		codec, kind, ok := CodecForStreamType(st)
		if !ok {
			codec, kind = "unknown", "unknown"
		}
		pmt.Tracks = append(pmt.Tracks, TrackDescription{
			PID: epid, StreamType: st, Codec: codec, Kind: kind,
		})
	}
	return pmt, nil
}
