// Package sample deterministically generates self-contained MPEG-TS fixtures
// (normal streams plus injected CC jumps/duplicates, PCR wrap, PAT/PMT version
// switch with a bad-CRC PMT, sync-byte corruption and a truncated tail).
// No external media files are used.
package sample

import (
	"encoding/binary"

	"mediapack/internal/ts"
)

// Well-known PIDs used by the generated fixture.
const (
	PIDPAT   uint16 = 0x0000
	PIDPMT   uint16 = 0x0100
	PIDVideo uint16 = 0x0101
	PIDAudio uint16 = 0x0102
)

// muxer accumulates fixed 188-byte packets and maintains per-PID CC.
type muxer struct {
	packets [][]byte
	nextCC  map[uint16]byte
}

func newMuxer() *muxer {
	return &muxer{nextCC: map[uint16]byte{}}
}

func (m *muxer) cc(pid uint16) byte {
	c := m.nextCC[pid]
	m.nextCC[pid] = (c + 1) & 0x0F
	return c
}

// skipCC advances the CC counter by extra-1 so the next emitted packet jumps
// forward by "jump" total steps (jump=2 => one counter value missing).
func (m *muxer) skipCC(pid uint16, jump byte) {
	cur := m.nextCC[pid]
	m.nextCC[pid] = (cur + jump - 1) & 0x0F
}

// duplicateCC returns the CC of the previous packet without advancing state.
func (m *muxer) duplicateCC(pid uint16) byte {
	return (m.nextCC[pid] - 1) & 0x0F
}

func buildRawPacket(pid uint16, pusi bool, cc byte, afc byte, adapt, payload []byte) []byte {
	raw := make([]byte, ts.PacketSize)
	raw[0] = ts.SyncByte
	b1 := byte((pid >> 8) & 0x1F)
	if pusi {
		b1 |= 0x40
	}
	raw[1] = b1
	raw[2] = byte(pid)
	raw[3] = (afc&0x03)<<4 | (cc & 0x0F)
	pos := 4
	if afc&0x02 != 0 {
		copy(raw[pos:], adapt)
		pos += len(adapt)
	}
	if afc&0x01 != 0 {
		copy(raw[pos:], payload)
		pos += len(payload)
		for ; pos < ts.PacketSize; pos++ {
			raw[pos] = 0xFF // payload padding
		}
	}
	return raw
}

// emit writes a normal (CC-advancing) packet and returns its packet index.
func (m *muxer) emit(pid uint16, pusi bool, afc byte, adapt, payload []byte) int {
	idx := len(m.packets)
	m.packets = append(m.packets, buildRawPacket(pid, pusi, m.cc(pid), afc, adapt, payload))
	return idx
}

// emitDuplicate writes a packet reusing the previous CC for the PID.
func (m *muxer) emitDuplicate(pid uint16, pusi bool, afc byte, adapt, payload []byte) int {
	idx := len(m.packets)
	m.packets = append(m.packets, buildRawPacket(pid, pusi, m.duplicateCC(pid), afc, adapt, payload))
	return idx
}

func pcrAdaptation(base uint64) []byte {
	a := make([]byte, 8)
	a[0] = 7    // adaptation_field_length after this byte
	a[1] = 0x10 // PCR flag
	b := a[2:8]
	b[0] = byte(base >> 25)
	b[1] = byte(base >> 17)
	b[2] = byte(base >> 9)
	b[3] = byte(base >> 1)
	b[4] = byte(base&1)<<7 | 0x7E // 6 reserved 1-bits + ext high bit(0)
	b[5] = 0x00                   // ext low 8 bits
	return a
}

func buildPES(streamID byte, pts, dts *uint64, payload []byte) []byte {
	var ptsBytes, dtsBytes []byte
	flags := byte(0x80)
	headerDataLen := 0
	if pts != nil && dts != nil {
		flags = 0xC0
		headerDataLen = 10
		pb := ts.EncodeTS(0x3, *pts)
		db := ts.EncodeTS(0x1, *dts)
		ptsBytes, dtsBytes = pb[:], db[:]
	} else if pts != nil {
		headerDataLen = 5
		pb := ts.EncodeTS(0x2, *pts)
		ptsBytes = pb[:]
	}
	pktLen := 3 + headerDataLen + len(payload)
	b := make([]byte, 0, 9+headerDataLen+len(payload))
	b = append(b, 0x00, 0x00, 0x01, streamID)
	b = append(b, byte(pktLen>>8), byte(pktLen))
	b = append(b, 0x80, flags, byte(headerDataLen))
	b = append(b, ptsBytes...)
	b = append(b, dtsBytes...)
	b = append(b, payload...)
	return b
}

func psiPacketPayload(section []byte) []byte {
	out := make([]byte, 0, 1+len(section))
	out = append(out, 0x00) // pointer_field
	out = append(out, section...)
	return out
}

func withCRC(body []byte) []byte {
	sec := make([]byte, 0, len(body)+4)
	sec = append(sec, body...)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], ts.CRC32MPEG(body))
	return append(sec, crc[:]...)
}

func buildPAT(version byte, pmtPID uint16) []byte {
	body := []byte{
		0x00,       // table_id PAT
		0xB0, 0x0D, // SI=1, reserved; section_length=13
		0x00, 0x01, // transport_stream_id
		0xC1 | (version&0x1F)<<1, // reserved + version + current_next=1
		0x00,                     // section_number
		0x00,                     // last_section_number
		0x00, 0x01,               // program_number 1
		byte(0xE0 | (pmtPID>>8)&0x1F), byte(pmtPID), // PMT PID
	}
	return withCRC(body)
}

func buildPMT(version byte, pcrPID, videoPID, audioPID uint16) []byte {
	body := []byte{
		0x02,       // table_id PMT
		0xB0, 0x17, // section_length=23
		0x00, 0x01, // program_number 1
		0xC1 | (version&0x1F)<<1,
		0x00, 0x00, // section / last
		byte(0xE0 | (pcrPID>>8)&0x1F), byte(pcrPID),
		0xF0, 0x00, // program_info_length 0
		0x1B, // stream_type H.264
		byte(0xE0 | (videoPID>>8)&0x1F), byte(videoPID),
		0xF0, 0x00, // ES_info_length 0
		0x0F, // stream_type AAC
		byte(0xE0 | (audioPID>>8)&0x1F), byte(audioPID),
		0xF0, 0x00,
	}
	return withCRC(body)
}

// corruptCRC flips bits in the trailing CRC of a section embedded in a packet.
func corruptPMTCRC(pkt []byte) {
	// payload starts at offset 4 (AFC=01); pointer_field at [4], section at 5;
	// locate the CRC as the last 4 non-padding bytes via section_length.
	secStart := 5
	if secStart+3 > len(pkt) {
		return
	}
	slen := int(uint16(pkt[secStart+1]&0x0F)<<8 | uint16(pkt[secStart+2]))
	crcPos := secStart + 3 + slen - 4
	if crcPos >= 0 && crcPos < ts.PacketSize {
		pkt[crcPos] ^= 0xFF
	}
}
