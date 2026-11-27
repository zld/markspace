// Package fixture: transport packet, PSI section and PES builders.
package fixture

import (
	"example.com/assetpack/internal/ts"
)

func buildPAT(version uint8, program, pmtPID uint16) []byte {
	body := []byte{byte(program >> 8), byte(program), 0xE0 | byte(pmtPID>>8), byte(pmtPID)}
	return section(ts.TableIDPAT, 0, version, body)
}

func buildPMT(version uint8, program, pcrPID, audioPID uint16, channels, rate int) []byte {
	// Register descriptors: ISO-639 "eng" plus a private descriptor carrying
	// the channel count and rate so a reader can verify what was declared.
	es := []byte{0x0F, 0xE0 | byte(audioPID>>8), byte(audioPID), 0xF0, 0x0B,
		0x0A, 0x03, 'e', 'n', 'g',
		0x05, 0x05, 0x41, byte(channels), byte(rate >> 16), byte(rate >> 8), byte(rate),
	}
	body := []byte{0xE1, byte(pcrPID), 0xF0, 0x00}
	body[0] = 0xE0 | byte(pcrPID>>8)
	body = append(body, es...)
	return section(ts.TableIDPMT, program, version, body)
}

func section(tableID uint8, ext uint16, version uint8, body []byte) []byte {
	sec := []byte{tableID}
	length := 5 + len(body) + 4
	sec = append(sec, byte(0xB0|length>>8), byte(length))
	sec = append(sec, byte(ext>>8), byte(ext))
	sec = append(sec, byte(0xC1|version<<1), 0x00, 0x00)
	sec = append(sec, body...)
	crc := ts.MPEGCRC32(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func packetizePSI(pid uint16, cc uint8, section []byte) []byte {
	payload := append([]byte{0}, section...)
	var out []byte
	first := true
	for len(payload) > 0 {
		n := 184
		if len(payload) < n {
			n = len(payload)
		}
		out = append(out, makePacket(pid, cc, first, nil, 0, payload[:n])...)
		payload = payload[n:]
		first = false
		cc = (cc + 1) & 0x0F
	}
	return out
}

func packetizePES(pid uint16, cc uint8, pusi bool, data []byte) []byte {
	return makePacket(pid, cc, pusi, nil, 0, data)
}

func buildAVPacket(pcrPID uint16, cc uint8, pcr uint64, randomAccess bool) []byte {
	return makePacket(pcrPID, cc, false, &pcr, 0, nil)
}

// makePacket builds a 188-byte packet.  A full 184-byte payload with no PCR
// uses adaptation_field_control 1, since an adaptation field would cost one
// byte for its own length and silently truncate the payload.
func makePacket(pid uint16, cc uint8, pusi bool, pcr *uint64, pcrExt uint16, payload []byte) []byte {
	if pcr == nil && len(payload) >= 184 {
		p := make([]byte, ts.PacketSize)
		p[0] = ts.SyncByte
		p[1] = byte(pid >> 8)
		if pusi {
			p[1] |= 0x40
		}
		p[2] = byte(pid)
		p[3] = 0x10 | cc
		copy(p[4:], payload[:184])
		return p
	}
	p := make([]byte, ts.PacketSize)
	p[0] = ts.SyncByte
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	afLen := 183 - len(payload)
	if pcr != nil {
		afLen -= 6
	}
	if afLen < 0 {
		afLen = 0
	}
	p[3] = 0x30 | cc
	p[4] = byte(afLen)
	pos := 5
	if afLen > 0 {
		flags := byte(0)
		if pcr != nil {
			flags |= ts.AFPCR
		}
		p[5] = flags
		pos = 6
		if pcr != nil {
			base := *pcr
			p[6] = byte(base >> 25)
			p[7] = byte(base >> 17)
			p[8] = byte(base >> 9)
			p[9] = byte(base >> 1)
			p[10] = byte(base<<7) | byte(pcrExt>>8)
			p[11] = byte(pcrExt)
			pos = 12
		}
		for pos < 5+afLen {
			p[pos] = 0xFF
			pos++
		}
	}
	copy(p[pos:], payload)
	return p
}

func buildPES(pts int64, payload []byte) []byte {
	header := []byte{0x00, 0x00, 0x01, 0xC0, 0x00, 0x00, 0x80, 0x80, 0x05}
	header = append(header, ts.PTSBytes(pts)...)
	return append(header, payload...)
}
