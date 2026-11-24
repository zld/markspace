// Package samplegen generates deterministic in-memory media, RTP, and TS
// fixtures. No external media files are used.
package samplegen

import (
	"encoding/binary"

	"medpipe/internal/ts"
)

// TSSpec describes a small deterministic TS fixture.
type TSSpec struct {
	PMTPID     uint16
	VideoPID   uint16
	AudioPID   uint16
	PATVersion uint8
	PMTVersion uint8
	Video      []SampleSpec
	Audio      []SampleSpec
	IncludePCR bool
}

// SampleSpec is one compressed sample and its raw 90 kHz PTS.
type SampleSpec struct {
	PTS      uint64
	Data     []byte
	Keyframe bool
}

type packetBuilder struct {
	cc map[uint16]uint8
}

// BuildTS builds a complete constant-rate TS byte stream.
func BuildTS(spec TSSpec) []byte {
	if spec.PMTPID == 0 {
		spec.PMTPID = 0x1000
	}
	if spec.VideoPID == 0 {
		spec.VideoPID = 0x0100
	}
	if spec.AudioPID == 0 {
		spec.AudioPID = 0x0101
	}
	b := &packetBuilder{cc: map[uint16]uint8{}}
	var out []byte

	pat := BuildPAT(spec.PATVersion, spec.PMTPID)
	out = append(out, b.psiPacket(0x0000, pat)...)
	pmt := BuildPMT(spec.PMTVersion, spec.PMTPID, spec.VideoPID, spec.AudioPID)
	out = append(out, b.psiPacket(spec.PMTPID, pmt)...)

	for _, s := range spec.Video {
		if spec.IncludePCR {
			out = append(out, b.pcrPacket(spec.VideoPID, s.PTS)...)
		}
		pes := BuildPES(0xe0, s.PTS, s.Data)
		for _, pkt := range b.mediaPackets(spec.VideoPID, pes) {
			out = append(out, pkt...)
		}
	}
	for _, s := range spec.Audio {
		pes := BuildPES(0xc0, s.PTS, s.Data)
		for _, pkt := range b.mediaPackets(spec.AudioPID, pes) {
			out = append(out, pkt...)
		}
	}
	return out
}

// BuildPAT builds a one-program PAT section including CRC.
func BuildPAT(version uint8, pmtPID uint16) []byte {
	s := []byte{
		0x00,       // table_id
		0xb0, 0x00, // syntax indicator + length placeholder
		0x12, 0x34, // transport_stream_id
		0xc0 | (version&0x1f)<<1 | 0x01,
		0x00, // section_number
		0x00, // last_section_number
	}
	program := make([]byte, 4)
	binary.BigEndian.PutUint16(program[0:2], 1)
	binary.BigEndian.PutUint16(program[2:4], 0xe000|pmtPID)
	s = append(s, program...)
	return finalizeSection(s)
}

// BuildPMT builds a one-video/one-audio PMT section including CRC.
func BuildPMT(version uint8, pmtPID, videoPID, audioPID uint16) []byte {
	s := []byte{
		0x02,       // table_id
		0xb0, 0x00, // length placeholder
		0x00, 0x01, // program_number
		0xc0 | (version&0x1f)<<1 | 0x01,
		0x00, 0x00, // section/last
		byte(0xe0 | (videoPID>>8)&0x1f), byte(videoPID), // PCR PID
		0xf0, 0x00, // program_info_length
	}
	v := []byte{ts.StreamTypeH264, byte(0xe0 | (videoPID>>8)&0x1f), byte(videoPID), 0xf0, 0x00}
	a := []byte{ts.StreamTypeAACADTS, byte(0xe0 | (audioPID>>8)&0x1f), byte(audioPID), 0xf0, 0x00}
	s = append(s, v...)
	s = append(s, a...)
	return finalizeSection(s)
}

func finalizeSection(prefix []byte) []byte {
	section := append([]byte(nil), prefix...)
	crc := ts.CRC32MPEG(section)
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	section = append(section, crcBytes...)
	sectionLen := len(section) - 3
	section[1] = 0xb0 | byte(sectionLen>>8&0x0f)
	section[2] = byte(sectionLen)
	// Length bytes changed after CRC calculation? They were placeholder 0x00
	// in the CRC-covered prefix, so recompute after setting real length.
	crc = ts.CRC32MPEG(section[:len(section)-4])
	binary.BigEndian.PutUint32(section[len(section)-4:], crc)
	return section
}

// BuildPES builds a PTS-only PES packet with declared length.
func BuildPES(streamID uint8, pts uint64, payload []byte) []byte {
	header := []byte{
		0x00, 0x00, 0x01, streamID,
		0x00, 0x00, // PES_packet_length patched below
		0x80, 0x80, 0x05,
		0x21 | byte(((pts>>30)&0x07)<<1),
		byte((pts >> 22) & 0xff),
		byte(((pts >> 15) & 0x7f) << 1),
		byte((pts >> 7) & 0xff),
		byte((pts&0x7f)<<1) | 0x01,
	}
	pesLen := 3 + 5 + len(payload)
	header[4] = byte(pesLen >> 8)
	header[5] = byte(pesLen)
	return append(header, payload...)
}

func (b *packetBuilder) nextCC(pid uint16) uint8 {
	cc := b.cc[pid]
	b.cc[pid] = (cc + 1) & 0x0f
	return cc
}

func (b *packetBuilder) psiPacket(pid uint16, section []byte) []byte {
	payload := append([]byte{0x00}, section...)
	for len(payload) < 184 {
		payload = append(payload, 0xff)
	}
	return b.packet(pid, true, 0x10, payload[:184])
}

func (b *packetBuilder) mediaPackets(pid uint16, pes []byte) [][]byte {
	var out [][]byte
	first := true
	for len(pes) > 0 {
		n := 184
		if n > len(pes) {
			n = len(pes)
		}
		pktPayload := pes[:n]
		pes = pes[n:]
		var pkt []byte
		if n == 184 {
			pkt = b.packet(pid, first, 0x10, pktPayload)
		} else {
			pkt = b.payloadPacketPadded(pid, first, pktPayload)
		}
		out = append(out, pkt)
		first = false
	}
	return out
}

func (b *packetBuilder) packet(pid uint16, pusi bool, afc byte, payload []byte) []byte {
	cc := b.nextCC(pid)
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	pkt[1] = byte((pid >> 8) & 0x1f)
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[2] = byte(pid)
	pkt[3] = afc | cc
	copy(pkt[4:], payload)
	return pkt
}

func (b *packetBuilder) payloadPacketPadded(pid uint16, pusi bool, payload []byte) []byte {
	// adaptation_field_length includes one flags byte plus 0xff stuffing.
	afLen := 183 - len(payload)
	cc := b.nextCC(pid)
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	pkt[1] = byte((pid >> 8) & 0x1f)
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[2] = byte(pid)
	pkt[3] = 0x30 | cc
	pkt[4] = byte(afLen)
	pkt[5] = 0x00 // no optional adaptation flags
	for i := 6; i < 5+afLen; i++ {
		pkt[i] = 0xff
	}
	copy(pkt[5+afLen:], payload)
	return pkt
}

func (b *packetBuilder) pcrPacket(pid uint16, pts uint64) []byte {
	cc := b.nextCC(pid)
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	pkt[1] = byte((pid >> 8) & 0x1f)
	pkt[2] = byte(pid)
	pkt[3] = 0x20 | cc
	pkt[4] = 183 // adaptation field fills the remainder
	pkt[5] = 0x10
	base := pts // fixture uses 90 kHz PTS as PCR_base
	pcr := pkt[6:12]
	pcr[0] = byte(base >> 25)
	pcr[1] = byte(base >> 17)
	pcr[2] = byte(base >> 9)
	pcr[3] = byte(base >> 1)
	pcr[4] = byte((base&1)<<7) | 0x7e
	pcr[5] = 0
	for i := 12; i < ts.PacketSize; i++ {
		pkt[i] = 0xff
	}
	return pkt
}
