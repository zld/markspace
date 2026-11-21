package slicer

import (
	"bytes"
	"encoding/binary"
	"time"

	"streamcheck/internal/ts"
)

func testTarget() time.Duration { return 3 * time.Second }

func byteReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// crcSection mirrors ts.CRC32MPEG2 through the public API by constructing a
// section and validating it; here we build raw section bytes directly.
func appendCRC(b []byte) []byte {
	crc := crc32(b)
	return append(b, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// crc32 duplicates the MPEG-2 polynomial for fixture construction.
func crc32(b []byte) uint32 {
	var crc uint32 = 0xffffffff
	for _, v := range b {
		crc ^= uint32(v) << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func buildPATSection() []byte {
	b := []byte{0x00, 0, 0, 0x01, 0x00, 0xc1, 0x00, 0x00,
		0x00, 0x01, byte(pmtPID>>8) | 0xe0, byte(pmtPID)}
	binary.BigEndian.PutUint16(b[1:3], uint16(len(b)+4-3)|0x3000)
	return appendCRC(b)
}

func buildPMTSection(vpid uint16) []byte {
	b := []byte{0x02, 0, 0,
		0x00, 0x01, // program_number
		0xc1, 0x00, 0x00,
		byte(vpid>>8) | 0xe0, byte(vpid), // PCR_PID
		0xf0, 0x00, // program_info_length 0
		ts.StreamTypeH264,
		byte(vpid>>8) | 0xe0, byte(vpid),
		0xf0, 0x00, // ES_info_length 0
	}
	binary.BigEndian.PutUint16(b[1:3], uint16(len(b)+4-3)|0x3000)
	return appendCRC(b)
}

// sectionPackets wraps a PSI section in TS packets with a pointer field.
func sectionPackets(pid uint16, section []byte) []byte {
	const cap = ts.PacketSize - 4
	var out []byte
	first := true
	cc := uint8(0)
	for len(section) > 0 || first {
		pkt := make([]byte, ts.PacketSize)
		pkt[0] = ts.SyncByte
		if first {
			pkt[1] = 0x40 | byte(pid>>8)
		} else {
			pkt[1] = byte(pid >> 8)
		}
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | (cc & 0x0f)
		cc++
		off := 4
		if first {
			pkt[4] = 0x00 // pointer_field
			off = 5
		}
		n := cap - (off - 4)
		if n > len(section) {
			n = len(section)
		}
		copy(pkt[off:off+n], section[:n])
		section = section[n:]
		out = append(out, pkt...)
		first = false
		if n == 0 {
			break
		}
	}
	return out
}

// encodePTS renders the 5-byte marker-padded timestamp.
func encodePTS(pts uint64) []byte {
	return []byte{
		0x21 | byte((pts>>30)&0x07)<<1,
		byte(pts >> 22),
		byte((pts>>14)&0xfe) | 0x01,
		byte(pts >> 7),
		byte((pts&0x7f)<<1) | 0x01,
	}
}

func mkVideoPES(pts uint64, payload []byte) []byte {
	opt := encodePTS(pts)
	hdr := []byte{0x00, 0x00, 0x01, 0xe0, 0, 0, 0x80, 0x00, byte(len(opt))}
	hdr = append(hdr, opt...)
	body := append(hdr, payload...)
	n := len(body) - 6
	body[4] = byte(n >> 8)
	body[5] = byte(n)
	return body
}

// pesPackets fragments a PES unit across TS packets; pusi marks the first.
func pesPackets(pid uint16, pusi bool, cc *uint8, pes []byte) []byte {
	const cap = ts.PacketSize - 4
	var out []byte
	first := true
	for len(pes) > 0 {
		pkt := make([]byte, ts.PacketSize)
		pkt[0] = ts.SyncByte
		if first && pusi {
			pkt[1] = 0x40 | byte(pid>>8)
		} else {
			pkt[1] = byte(pid >> 8)
		}
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | (*cc & 0x0f)
		*cc++
		n := cap
		if n > len(pes) {
			n = len(pes)
		}
		copy(pkt[4:4+n], pes[:n])
		pes = pes[n:]
		out = append(out, pkt...)
		first = false
	}
	return out
}
