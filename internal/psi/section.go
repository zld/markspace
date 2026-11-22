// Package psi implements the self-contained PSI parser: MPEG-2 CRC
// validation, section header parsing and reassembly of sections that
// are fragmented across TS packets. Only complete, CRC-valid sections
// are handed to the table assembler.
package psi

import "fmt"

// MaxSectionLength bounds a PSI section (syntax allows 4093).
const MaxSectionLength = 4096

// crcTable is the MPEG-2 CRC-32 table (poly 0x04C11DB7, init 0xFFFFFFFF,
// no reflection, no final XOR).
var crcTable = func() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// CRC computes the MPEG-2 CRC-32 of b. A well-formed section including
// its trailing CRC checksum evaluates to zero.
func CRC(b []byte) uint32 {
	c := uint32(0xFFFFFFFF)
	for _, v := range b {
		c = c<<8 ^ crcTable[byte(c>>24)^v]
	}
	return c
}

// Header is the parsed PSI section header (long form).
type Header struct {
	TableID           uint8
	SectionLength     int // bytes following the section_length field
	Version           int // 5-bit version_number
	SectionNumber     int
	LastSectionNumber int
	TableIDExtension  int // transport_stream_id (PAT) / program_number (PMT)
}

// TotalLength is the full section size including header and CRC.
func (h Header) TotalLength() int { return 3 + h.SectionLength }

// ParseHeader parses the long-form section header at the start of b.
// b must hold at least the 8 header bytes.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < 8 {
		return Header{}, fmt.Errorf("section header truncated: %d bytes", len(b))
	}
	if b[1]&0x80 == 0 {
		return Header{}, fmt.Errorf("table_id 0x%02x: section_syntax_indicator not set", b[0])
	}
	h := Header{
		TableID:           b[0],
		SectionLength:     int(b[1]&0x0F)<<8 | int(b[2]),
		TableIDExtension:  int(b[3])<<8 | int(b[4]),
		Version:           int(b[5]>>1) & 0x1F,
		SectionNumber:     int(b[6]),
		LastSectionNumber: int(b[7]),
	}
	if h.SectionLength < 5+4 || h.TotalLength() > MaxSectionLength {
		return Header{}, fmt.Errorf("table_id 0x%02x: invalid section_length %d", h.TableID, h.SectionLength)
	}
	return h, nil
}

// ValidCRC reports whether the complete section passes its MPEG-2 CRC.
func ValidCRC(section []byte) bool {
	return len(section) >= 4 && CRC(section) == 0
}

// Reassembler collects PSI sections on one PID, handling fragmentation
// across packets, multiple sections per packet and pointer_field
// alignment. Complete sections are passed to emit in arrival order.
type Reassembler struct {
	buf []byte // partial section bytes awaiting continuation
}

// Reset drops any partially reassembled section.
func (r *Reassembler) Reset() { r.buf = nil }

// Pending reports whether a partial section is buffered.
func (r *Reassembler) Pending() bool { return len(r.buf) > 0 }

// PendingMissing returns how many bytes the buffered partial section
// still needs, or 0 when no partial section is buffered or its header
// is not complete yet.
func (r *Reassembler) PendingMissing() int {
	if len(r.buf) < 3 {
		return 0
	}
	total := 3 + ((int(r.buf[1]&0x0F) << 8) | int(r.buf[2]))
	if total > len(r.buf) {
		return total - len(r.buf)
	}
	return 0
}

// Feed consumes one packet payload. pusi must be the packet's payload
// unit start indicator. Corrupt fragments are dropped; a gap in the
// transport never discards later, well-formed sections.
func (r *Reassembler) Feed(pusi bool, payload []byte, emit func([]byte)) {
	if len(payload) == 0 {
		return
	}
	data := payload
	if pusi {
		pointer := int(data[0])
		data = data[1:]
		if pointer > len(data) {
			r.Reset() // pointer beyond payload: corrupt, resync
			return
		}
		if pointer > 0 {
			// Complete the previous section with the bytes before the pointer.
			if len(r.buf) > 0 {
				r.buf = append(r.buf, data[:pointer]...)
				r.drain(emit)
			}
			data = data[pointer:]
		}
		r.buf = nil
	} else if len(r.buf) == 0 {
		return // continuation without a start: cannot align, skip
	}
	r.buf = append(r.buf, data...)
	r.drain(emit)
}

// drain emits every complete section at the front of the buffer and
// keeps the trailing partial section (if any).
func (r *Reassembler) drain(emit func([]byte)) {
	for {
		// Skip stuffing between sections.
		for len(r.buf) > 0 && r.buf[0] == 0xFF {
			r.buf = r.buf[1:]
		}
		if len(r.buf) < 3 {
			return
		}
		total := 3 + ((int(r.buf[1]&0x0F) << 8) | int(r.buf[2]))
		if total > MaxSectionLength || total < 3+5+4 {
			r.buf = nil // implausible length: drop and resync on next PUSI
			return
		}
		if len(r.buf) < total {
			return // wait for continuation packets
		}
		emit(r.buf[:total])
		r.buf = r.buf[total:]
	}
}
