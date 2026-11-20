// Package psi reassembles and validates PSI sections (PAT/PMT) from TS
// packet payloads, handling pointer_field, sections spanning packets and
// multiple sections per packet.
package psi

import (
	"errors"
	"fmt"

	"tsmap/internal/ts"
)

// MaxSectionLength bounds a full section (3-byte header + section_length).
// section_length is 12 bits, so 4099 is the hard protocol maximum; the cap
// also prevents corrupt lengths from causing unbounded allocation.
const MaxSectionLength = 3 + 0xFFF

// ErrInvalidSection marks a malformed or CRC-failing section.
var ErrInvalidSection = errors.New("psi: invalid section")

// Section is one complete, CRC-validated PSI section.
type Section struct {
	PID     uint16
	Offset  int64 // file offset of the packet that completed the section
	TableID byte
	Data    []byte // 3-byte header through trailing CRC32
}

// TableIDExtension returns the table_id_extension (e.g. TSID for PAT,
// program number for PMT).
func (s *Section) TableIDExtension() uint16 {
	return uint16(s.Data[3])<<8 | uint16(s.Data[4])
}

// Version returns the 5-bit version_number.
func (s *Section) Version() byte { return (s.Data[5] >> 1) & 0x1f }

// CurrentNext reports the current_next_indicator bit.
func (s *Section) CurrentNext() bool { return s.Data[6]&1 != 0 }

// SectionNumber returns the section_number byte.
func (s *Section) SectionNumber() byte { return s.Data[6] }

// LastSectionNumber returns the last_section_number byte.
func (s *Section) LastSectionNumber() byte { return s.Data[7] }

// CRCValid reports whether the MPEG-2 CRC32 over the whole section is zero.
func CRCValid(b []byte) bool { return crc32mpeg(b) == 0 }

// Checksum returns the MPEG-2 CRC32 of b (used to build sections).
func Checksum(b []byte) uint32 { return crc32mpeg(b) }

var crcTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		crcTable[i] = c
	}
}

func crc32mpeg(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^x]
	}
	return crc
}

type partial struct {
	buf  []byte
	need int // expected total length, 0 while header incomplete
}

// Assembler reassembles sections per PID. Feed packets in file order.
type Assembler struct {
	per map[uint16]*partial
}

// NewAssembler returns an empty Assembler.
func NewAssembler() *Assembler { return &Assembler{per: make(map[uint16]*partial)} }

// Feed consumes the payload of one packet on a PSI PID and returns every
// section completed by it. Malformed data yields an error naming the
// offset; the PID's partial state is reset so assembly can continue.
func (a *Assembler) Feed(p *ts.Packet) ([]Section, error) {
	payload := p.Payload
	if len(payload) == 0 {
		return nil, nil
	}
	st := a.per[p.PID]
	if st == nil {
		st = &partial{}
		a.per[p.PID] = st
	}
	var out []Section
	if p.PUSI {
		pointer := int(payload[0])
		if pointer > len(payload)-1 {
			a.reset(p.PID)
			return nil, fmt.Errorf("%w: pointer_field %d overruns payload at offset %d",
				ErrInvalidSection, pointer, p.Offset)
		}
		// Bytes skipped by pointer_field belong to any in-flight section.
		if pointer > 0 && len(st.buf) > 0 {
			var secs []Section
			secs, err := a.append(p.PID, st, payload[1:1+pointer], p.Offset)
			if err != nil {
				return nil, err
			}
			out = append(out, secs...)
		}
		payload = payload[1+pointer:]
		if len(payload) > 0 && payload[0] == 0xFF {
			return out, nil // stuffing only
		}
	}
	secs, err := a.append(p.PID, st, payload, p.Offset)
	if err != nil {
		return nil, err
	}
	out = append(out, secs...)
	return out, nil
}

func (a *Assembler) reset(pid uint16) { a.per[pid] = &partial{} }

// append feeds bytes into the per-PID reassembly buffer, emitting sections
// as they complete. Several sections may share one packet.
func (a *Assembler) append(pid uint16, st *partial, data []byte, off int64) ([]Section, error) {
	var out []Section
	st.buf = append(st.buf, data...)
	for {
		if st.need == 0 {
			if len(st.buf) == 0 {
				return out, nil
			}
			if st.buf[0] == 0xFF { // stuffing
				st.buf = st.buf[:0]
				return out, nil
			}
			if len(st.buf) < 3 {
				return out, nil
			}
			sl := int(st.buf[1]&0x0f)<<8 | int(st.buf[2])
			if sl < 4 || 3+sl > MaxSectionLength {
				a.reset(pid)
				return nil, fmt.Errorf("%w: bad section_length %d at offset %d", ErrInvalidSection, sl, off)
			}
			st.need = 3 + sl
		}
		if len(st.buf) < st.need {
			return out, nil
		}
		sec := st.buf[:st.need]
		if !CRCValid(sec) {
			a.reset(pid)
			return nil, fmt.Errorf("%w: CRC mismatch (table_id 0x%02x) at offset %d",
				ErrInvalidSection, sec[0], off)
		}
		cp := make([]byte, len(sec))
		copy(cp, sec)
		out = append(out, Section{PID: pid, Offset: off, TableID: sec[0], Data: cp})
		st.buf = append([]byte(nil), st.buf[st.need:]...)
		st.need = 0
	}
}
