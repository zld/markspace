package ts

import (
	"encoding/binary"
	"errors"
)

// Well-known table identifiers and stream types.
const (
	tableIDPAT byte = 0x00
	tableIDPMT byte = 0x02

	// StreamTypeH264 is ITU-T Rec. H.264 / AVC video.
	StreamTypeH264 byte = 0x1b
	// StreamTypeADTS is AAC audio in an ADTS framing.
	StreamTypeADTS byte = 0x0f
	// StreamTypeAAC is AAC audio carried as generic PES.
	StreamTypeAAC byte = 0x11
)

// crcMPEG2Table is the IEEE 802.3 / MPEG-2 CRC-32 lookup table.
var crcMPEG2Table = func() [256]uint32 {
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

// CRC32MPEG2 computes the CRC-32 used by PSI sections (poly 0x04c11db7,
// init 0xFFFFFFFF, no reflection, no final XOR).
func CRC32MPEG2(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, v := range b {
		crc = (crc << 8) ^ crcMPEG2Table[byte(crc>>24)^v]
	}
	return crc
}

// Section is one reassembled PSI table section.
type Section struct {
	TableID       byte
	SyntaxSection bool
	Version       byte
	Current       bool
	Number        byte
	Last          byte
	Data          []byte // full section bytes including the 32-bit CRC
}

// SectionLength reports the value of the section_length field.
func (s Section) SectionLength() int { return len(s.Data) - 3 }

// AssembleSection collects section payload bytes from packets on one PID.
// It returns the full section once the last packet of the section arrives;
// incomplete input returns nil without error.
type SectionAssembler struct {
	buf       []byte
	remaining int // payload bytes still expected, 0 when idle
	pusiSeen  bool
}

// Push consumes one packet's payload. A complete section is returned once,
// after which the assembler is ready for the next table.
func (a *SectionAssembler) Push(p Packet) *Section {
	if p.PID == PIDNull {
		return nil
	}
	pl := p.Payload()
	if len(pl) == 0 {
		return nil
	}
	if p.PUSI {
		// pointer_field precedes the first section in this packet; any
		// bytes before the pointer are the tail of a previous section,
		// which the assembler intentionally discards.
		ptr := int(pl[0])
		start := 1 + ptr
		if start > len(pl) {
			return nil
		}
		pl = pl[start:]
		a.buf = a.buf[:0]
		a.pusiSeen = true
		if len(pl) >= 3 {
			n := sectionLen(pl)
			// PSI sections cannot exceed 1021 bytes of section_length.
			if n < 5 || n > 1021 {
				a.pusiSeen = false
				return nil
			}
			a.remaining = 3 + n
		}
	}
	if !a.pusiSeen || len(pl) == 0 {
		return nil
	}
	take := len(pl)
	if a.remaining > 0 && take > a.remaining {
		take = a.remaining
	}
	if take <= 0 {
		return nil
	}
	a.buf = append(a.buf, pl[:take]...)
	if a.remaining > 0 {
		a.remaining -= take
	}
	if a.remaining != 0 {
		return nil
	}
	sec := parseSectionHeader(a.buf)
	a.buf = a.buf[:0]
	a.pusiSeen = false
	return &sec
}

// sectionLen reads section_length from the start of a section body.
func sectionLen(b []byte) int {
	return int(binary.BigEndian.Uint16(b[1:3]) & 0x0fff)
}

func parseSectionHeader(b []byte) Section {
	return Section{
		TableID:       b[0],
		SyntaxSection: b[1]&0x80 != 0,
		Version:       (b[5] >> 1) & 0x1f,
		Current:       b[5]&0x01 != 0,
		Number:        b[6],
		Last:          b[7],
		Data:          append([]byte(nil), b...),
	}
}

// CheckCRC validates the trailing CRC-32 against the section bytes.
func (s Section) CheckCRC() error {
	if len(s.Data) < 4 {
		return errors.New("ts: section too short for CRC")
	}
	body := s.Data[:len(s.Data)-4]
	want := binary.BigEndian.Uint32(s.Data[len(s.Data)-4:])
	if got := CRC32MPEG2(body); got != want {
		return errors.New("ts: section CRC mismatch")
	}
	return nil
}

// Program maps one PAT entry: program number to its PMT PID.
type Program struct {
	Number uint16
	PMTPID uint16
}

// ParsePAT extracts programs from a PAT section and verifies its CRC.
func ParsePAT(s Section) ([]Program, error) {
	if s.TableID != tableIDPAT {
		return nil, errors.New("ts: not a PAT section")
	}
	if err := s.CheckCRC(); err != nil {
		return nil, err
	}
	var progs []Program
	// Header occupies 8 bytes; the 4-byte CRC terminates the section.
	i := 8
	end := len(s.Data) - 4
	for i+4 <= end {
		num := binary.BigEndian.Uint16(s.Data[i : i+2])
		pid := binary.BigEndian.Uint16(s.Data[i+2:i+4]) & 0x1fff
		i += 4
		if num == 0 {
			continue // network PID entry
		}
		progs = append(progs, Program{Number: num, PMTPID: pid})
	}
	return progs, nil
}

// ESInfo describes one elementary stream declared in a PMT.
type ESInfo struct {
	StreamType byte
	EPID       uint16
}

// IsVideo reports whether the stream type denotes H.264 video.
func (e ESInfo) IsVideo() bool { return e.StreamType == StreamTypeH264 }

// IsAudio reports whether the stream type denotes AAC audio.
func (e ESInfo) IsAudio() bool {
	return e.StreamType == StreamTypeADTS || e.StreamType == StreamTypeAAC
}

// PMT is one parsed program map table.
type PMT struct {
	ProgramNumber uint16
	PCRPID        uint16
	Streams       []ESInfo
}

// VideoPID returns the first H.264 elementary PID, or 0 if none.
func (p PMT) VideoPID() uint16 {
	for _, s := range p.Streams {
		if s.IsVideo() {
			return s.EPID
		}
	}
	return 0
}

// AudioPID returns the first AAC elementary PID, or 0 if none.
func (p PMT) AudioPID() uint16 {
	for _, s := range p.Streams {
		if s.IsAudio() {
			return s.EPID
		}
	}
	return 0
}

// ParsePMT extracts elementary streams from a PMT section and checks CRC.
func ParsePMT(s Section) (PMT, error) {
	if s.TableID != tableIDPMT {
		return PMT{}, errors.New("ts: not a PMT section")
	}
	if err := s.CheckCRC(); err != nil {
		return PMT{}, err
	}
	if len(s.Data) < 16 {
		return PMT{}, errors.New("ts: PMT section too short")
	}
	// Section layout: table_id(1) + length(2) + program_number(2) +
	// version/section(3) + PCR_PID(2) + program_info_length(2).
	pmt := PMT{
		ProgramNumber: binary.BigEndian.Uint16(s.Data[3:5]),
		PCRPID:        binary.BigEndian.Uint16(s.Data[8:10]) & 0x1fff,
	}
	progInfoLen := int(binary.BigEndian.Uint16(s.Data[10:12]) & 0x0fff)
	i := 12 + progInfoLen
	end := len(s.Data) - 4
	for i+5 <= end {
		esLen := int(binary.BigEndian.Uint16(s.Data[i+3:i+5]) & 0x0fff)
		pmt.Streams = append(pmt.Streams, ESInfo{
			StreamType: s.Data[i],
			EPID:       binary.BigEndian.Uint16(s.Data[i+1:i+3]) & 0x1fff,
		})
		i += 5 + esLen
	}
	return pmt, nil
}
