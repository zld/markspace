package ts

import (
	"tsrepair/internal/mediaerr"
)

// pointerFieldExtra is the single pointer_field byte prepended to PSI payloads
// when PUSI is set.
const pointerFieldExtra = 1

// sectionAssembler collects section fragments for one table PID. A new
// section starts on a PUSI packet; subsequent packets append until
// section_length bytes are complete. Oversized or truncated sections are
// rejected and the partial buffer is flushed.
type sectionAssembler struct {
	pid       uint16
	buf       []byte
	remaining int // bytes still expected after buf, 0 when idle
}

// push feeds one packet payload. It returns zero or one complete section
// (excluding the pointer field). The returned slice is a copy and remains
// valid after the next call.
func (a *sectionAssembler) push(p *Packet, ec *mediaerr.Collector) [][]byte {
	var out [][]byte
	payload := p.Payload
	if p.PUSI {
		if a.remaining > 0 {
			ec.Add(mediaerr.Recoverable(mediaerr.CodeSectionMissing, a.pid, p.Offset,
				"new section starts before previous section completed",
				"flush incomplete section"))
			a.buf = nil
			a.remaining = 0
		}
		if len(payload) == 0 {
			ec.Add(mediaerr.Fatal(mediaerr.CodeSectionMissing, a.pid, p.Offset,
				"PUSI packet with empty PSI payload", "skip packet"))
			return nil
		}
		pointer := int(payload[0])
		if pointer+1 > len(payload) {
			ec.Add(mediaerr.Fatal(mediaerr.CodeSectionTooLong, a.pid, p.Offset,
				"pointer field exceeds payload", "skip packet"))
			return nil
		}
		// Trailing bytes after the pointer offset belong to the previous
		// section, which we already flushed; ignore them.
		payload = payload[1+pointer:]
		a.startSection(payload, p.Offset, ec)
	} else if a.remaining > 0 {
		a.append(payload)
	}
	if a.remaining == 0 && len(a.buf) > 0 {
		section := make([]byte, len(a.buf))
		copy(section, a.buf)
		a.buf = nil
		out = append(out, section)
	}
	return out
}

func (a *sectionAssembler) startSection(payload []byte, off int64, ec *mediaerr.Collector) {
	// Minimum section: 8-byte header including table_id through
	// section_length, plus 4 CRC bytes.
	if len(payload) < 3 {
		ec.Add(mediaerr.Fatal(mediaerr.CodeSectionMissing, a.pid, off,
			"section header truncated", "flush section"))
		return
	}
	sectionLen := int(payload[1]&0x0F)<<8 | int(payload[2])
	if sectionLen < 9 {
		ec.Add(mediaerr.Fatal(mediaerr.CodeSectionTooLong, a.pid, off,
			"section_length below 9-byte minimum", "flush section"))
		return
	}
	total := 3 + sectionLen // section_length counts bytes after its own field
	if total > MaxSectionLength {
		ec.Add(mediaerr.Fatal(mediaerr.CodeSectionTooLong, a.pid, off,
			"section exceeds maximum allowed length", "flush section"))
		return
	}
	if len(payload) > total {
		// Padding or the start of the next section share this PUSI packet;
		// keep only the bytes that belong to this section.
		payload = payload[:total]
	}
	a.buf = append(a.buf, payload...)
	a.remaining = total - len(payload)
	if a.remaining < 0 {
		a.remaining = 0
	}
}

func (a *sectionAssembler) append(payload []byte) {
	take := a.remaining
	if take > len(payload) {
		take = len(payload)
	}
	a.buf = append(a.buf, payload[:take]...)
	a.remaining -= take
}

// crc32MPEG2 computes the ISO 13818-1 CRC-32 (polynomial 0x04C11DB7, no
// reflection, init/final xor 0xFFFFFFFF).
func crc32MPEG2(data []byte) uint32 {
	var crc uint32 = 0xFFFFFFFF
	for _, b := range data {
		crc ^= uint32(b) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// verifyCRC checks the trailing CRC32 of a complete PSI section. The CRC is
// computed over all section bytes including the 4-byte CRC field, which must
// yield the MPEG-2 check residue 0.
func verifyCRC(section []byte) bool {
	if len(section) < 4 {
		return false
	}
	return crc32MPEG2(section) == 0
}
