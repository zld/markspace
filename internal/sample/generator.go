// Package sample generates the deterministic MPEG-TS fixtures used by the
// acceptance demo and the tests. Every byte is produced in-process from the
// ts package's constants and exported CRC routine; no external media is read.
package sample

import (
	"encoding/binary"

	"mediad/internal/clock"
	"mediad/ts"
)

// Well-known PIDs used by every generated program.
const (
	PMTPID   = uint16(0x1000)
	VideoPID = uint16(0x0101)
	// PayloadBytes is the fixed per-packet elementary payload size.
	PayloadBytes = 100
	// PacketTicks is one 90 kHz millisecond per packet.
	PacketTicks = int64(90)
)

// Kind names a generated fixture.
type Kind string

const (
	KindNormal    Kind = "normal"
	KindWrap      Kind = "pcr_wrap"
	KindJump      Kind = "cc_jump"
	KindPSIUpdate Kind = "psi_update"
)

// section wraps a table body with pointer, header and a real CRC.
func section(tableID uint8, ext uint16, body []byte, version uint8) []byte {
	sectionLen := uint16(5 + len(body) + 4)
	head := []byte{
		tableID,
		0xb0 | byte(sectionLen>>8), byte(sectionLen),
		byte(ext >> 8), byte(ext),
		0xc0 | (version << 1) | 0x01, // version 5 bits + current/next indicator
		0x00, 0x00,
	}
	raw := append(head, body...)
	crc := ts.CRC32MPEG2(raw)
	crcB := make([]byte, 4)
	binary.BigEndian.PutUint32(crcB, crc)
	return append([]byte{0x00}, append(raw, crcB...)...)
}

func patPacket(cc uint8, pmtPID uint16, version uint8) []byte {
	body := make([]byte, 4)
	binary.BigEndian.PutUint16(body[0:], 1) // program 1
	binary.BigEndian.PutUint16(body[2:], 0xe000|pmtPID)
	return tablePacket(ts.PIDPAT, true, cc, section(0x00, 1, body, version))
}

func pmtPacket(cc uint8, videoPID uint16, version uint8) []byte {
	body := []byte{
		0xe0 | byte(videoPID>>8), byte(videoPID), // PCR PID
		0xf0, 0x00, // program info length 0
		0x1b, // stream type: H.264 video
		0xe0 | byte(videoPID>>8), byte(videoPID),
		0xf0, 0x00, // ES info length 0
	}
	return tablePacket(PMTPID, true, cc, section(0x02, 1, body, version))
}

// tablePacket wraps a PSI payload in a full 188-byte packet.
func tablePacket(pid uint16, pusi bool, cc uint8, payload []byte) []byte {
	b := make([]byte, ts.PacketSize)
	b[0] = ts.SyncByte
	if pusi {
		b[1] |= 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = 0x10 | (cc & 0x0f) // payload only
	copy(b[4:], payload)
	return b
}

// videoPacket builds an adaptation+payload packet carrying pcrBase.
func videoPacket(cc uint8, pcrBase int64, payloadMarker byte) []byte {
	b := make([]byte, ts.PacketSize)
	b[0] = ts.SyncByte
	b[1] = byte(VideoPID >> 8)
	b[2] = byte(VideoPID & 0xff)
	b[3] = 0x30 | (cc & 0x0f) // adaptation + payload
	b[4] = 7                  // adaptation field length
	b[5] = 0x10               // PCR flag
	v := uint64(pcrBase)
	b[6] = byte(v >> 25)
	b[7] = byte(v >> 17)
	b[8] = byte(v >> 9)
	b[9] = byte(v >> 1)
	b[10] = byte(v<<7) & 0xfe
	b[11] = 0
	for i := 12; i < ts.PacketSize; i++ {
		b[i] = payloadMarker
	}
	return b
}

// Generate returns one named fixture and the number of video packets it holds.
func Generate(kind Kind) (data []byte, videoPackets int) {
	var out []byte
	out = append(out, patPacket(0, PMTPID, 1)...)
	out = append(out, pmtPacket(0, VideoPID, 1)...)

	const n = 40
	switch kind {
	case KindWrap:
		// Start close to the 33-bit ceiling so the monotonic PCR wraps.
		start := clock.Mod33 - 10*PacketTicks
		for i := 0; i < n; i++ {
			out = append(out, videoPacket(uint8(i%16), start+int64(i)*PacketTicks, byte(0xa0+i))...)
		}
	case KindJump:
		for i := 0; i < n; i++ {
			cc := uint8(i % 16)
			if i >= 10 {
				cc += 3 // deterministic continuity jump at packet 10
			}
			out = append(out, videoPacket(cc&0x0f, int64(i)*PacketTicks, byte(0xa0+i))...)
		}
	case KindPSIUpdate:
		// Mid-stream: a corrupt section (rejected) then valid version 2 maps.
		for i := 0; i < n; i++ {
			if i == 12 {
				bad := patPacket(1, PMTPID, 2)
				// Corrupt a section body byte (not the CRC field) so the
				// recomputed CRC32 genuinely mismatches and the table is
				// rejected before the valid version-2 mapping follows.
				bad[12] ^= 0xff
				out = append(out, bad...)
				out = append(out, patPacket(2, PMTPID, 2)...)
				out = append(out, pmtPacket(1, VideoPID, 2)...)
			}
			out = append(out, videoPacket(uint8(i%16), int64(i)*PacketTicks, byte(0xa0+i))...)
		}
	default: // KindNormal
		for i := 0; i < n; i++ {
			out = append(out, videoPacket(uint8(i%16), int64(i)*PacketTicks, byte(0xa0+i))...)
		}
	}
	return out, n
}

// Truncate returns a normal fixture whose final packet is cut to a partial
// tail, together with the byte offset where truncation begins.
func Truncate() (data []byte, truncOffset int64) {
	full, _ := Generate(KindNormal)
	truncOffset = int64(len(full) - ts.PacketSize)
	return full[:truncOffset+7], truncOffset // 7 trailing bytes of a cut packet
}

// AllKinds lists every generated fixture in demo order.
func AllKinds() []Kind {
	return []Kind{KindNormal, KindWrap, KindJump, KindPSIUpdate}
}
