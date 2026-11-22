// Package fixture deterministically generates local MPEG-TS samples
// containing a program switch, PID remapping and controlled packet
// loss. No external files are referenced.
package fixture

import (
	"io"

	"tsaudit/internal/psi"
)

// Packet builds one 188-byte TS packet with payload, stuffing the rest
// with 0xFF.
func Packet(pid uint16, pusi bool, cc uint8, payload []byte) []byte {
	if len(payload) > 184 {
		panic("fixture: payload too large")
	}
	b := make([]byte, 188)
	b[0] = 0x47
	b[1] = byte(pid >> 8)
	if pusi {
		b[1] |= 0x40
	}
	b[2] = byte(pid)
	b[3] = 0x10 | (cc & 0x0F) // payload only, no adaptation field
	n := copy(b[4:], payload)
	for i := 4 + n; i < 188; i++ {
		b[i] = 0xFF
	}
	return b
}

// PATSection builds a CRC-valid PAT section. Each entry is
// {program_number, pmt_pid}.
func PATSection(version uint8, tsid uint16, entries ...[2]uint16) []byte {
	secLen := 5 + 4*len(entries) + 4
	s := []byte{
		0x00, 0xB0 | byte(secLen>>8), byte(secLen),
		byte(tsid >> 8), byte(tsid),
		0xC0 | (version&0x1F)<<1 | 0x01, // reserved, version, current_next
		0x00, 0x00, // section 0 of 0
	}
	for _, e := range entries {
		s = append(s, byte(e[0]>>8), byte(e[0]), 0xE0|byte(e[1]>>8), byte(e[1]))
	}
	return appendCRC(s)
}

// PMTSection builds a CRC-valid PMT section. Each stream is
// {stream_type, elementary_pid}.
func PMTSection(version uint8, program, pcrPID uint16, streams ...[2]uint16) []byte {
	secLen := 9 + 5*len(streams) + 4
	s := []byte{
		0x02, 0xB0 | byte(secLen>>8), byte(secLen),
		byte(program >> 8), byte(program),
		0xC0 | (version&0x1F)<<1 | 0x01,
		0x00, 0x00,
		0xE0 | byte(pcrPID>>8), byte(pcrPID),
		0xF0, 0x00, // program_info_length = 0
	}
	for _, st := range streams {
		s = append(s, byte(st[0]), 0xE0|byte(st[1]>>8), byte(st[1]), 0xF0, 0x00)
	}
	return appendCRC(s)
}

// PSIPacket wraps one section in a single TS packet with pointer_field 0.
func PSIPacket(pid uint16, cc uint8, section []byte) []byte {
	return Packet(pid, true, cc, append([]byte{0x00}, section...))
}

func appendCRC(s []byte) []byte {
	crc := psi.Checksum(s)
	return append(s, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// esPayload builds deterministic filler for an elementary stream packet.
func esPayload(pid uint16, cc uint8) []byte {
	b := make([]byte, 184)
	for i := range b {
		b[i] = byte(int(pid) + int(cc) + i)
	}
	return b
}

// Generate writes the standard acceptance sample: program 1 first maps
// to PMT PID 100 (video 256 = H.264, audio 257 = AAC), then the PAT
// remaps it to PMT PID 110 (video 300, audio still 257). Each mapping
// governs 3 video and 2 audio packets. PID 256's continuity counter
// jumps 0 -> 2, accounting for exactly one missing packet.
func Generate(w io.Writer) error {
	pkts := [][]byte{
		PSIPacket(0, 0, PATSection(0, 1, [2]uint16{1, 100})),
		PSIPacket(100, 0, PMTSection(0, 1, 256,
			[2]uint16{0x1B, 256}, [2]uint16{0x0F, 257})),
		Packet(256, true, 0, esPayload(256, 0)),
		Packet(256, false, 2, esPayload(256, 2)), // CC jump: 1 missing
		Packet(256, false, 3, esPayload(256, 3)),
		Packet(257, true, 0, esPayload(257, 0)),
		Packet(257, false, 1, esPayload(257, 1)),
		PSIPacket(0, 1, PATSection(1, 1, [2]uint16{1, 110})),
		PSIPacket(110, 0, PMTSection(0, 1, 300,
			[2]uint16{0x1B, 300}, [2]uint16{0x0F, 257})),
		Packet(300, true, 0, esPayload(300, 0)),
		Packet(300, false, 1, esPayload(300, 1)),
		Packet(300, false, 2, esPayload(300, 2)),
		Packet(257, false, 2, esPayload(257, 2)),
		Packet(257, false, 3, esPayload(257, 3)),
	}
	for _, p := range pkts {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}
