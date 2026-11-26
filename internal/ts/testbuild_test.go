package ts

import (
	"bytes"
	"context"

	"rtpregress/internal/clock"
)

// This file builds minimal, spec-valid MPEG-TS packets for parser tests.
// All multi-byte fields are big-endian; PSI sections carry a real CRC32.

func tsPacket(pid uint16, pusi, hasAF, hasPayload bool, cc uint8, body []byte) []byte {
	pkt := make([]byte, PacketSize)
	pkt[0] = syncByte
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[1] |= byte(pid>>8) & 0x1F
	pkt[2] = byte(pid)
	if hasAF {
		pkt[3] |= 0x20
	}
	if hasPayload {
		pkt[3] |= 0x10
	}
	pkt[3] |= cc & 0x0F
	copy(pkt[4:], body)
	return pkt
}

func psiSection(tableID uint8, idExt uint16, version uint8, loop []byte) []byte {
	inner := 5 + len(loop) + 4 // syntax header + loop + CRC
	sec := make([]byte, 3+inner)
	sec[0] = tableID
	sec[1] = 0xB0 | byte(inner>>8)
	sec[2] = byte(inner)
	sec[3] = byte(idExt >> 8)
	sec[4] = byte(idExt)
	sec[5] = 0xC1 | (version&0x1F)<<1 // current=1, section=0
	sec[6], sec[7] = 0, 0
	copy(sec[8:], loop)
	crc := crc32mpeg2(sec[:len(sec)-4])
	sec[len(sec)-4] = byte(crc >> 24)
	sec[len(sec)-3] = byte(crc >> 16)
	sec[len(sec)-2] = byte(crc >> 8)
	sec[len(sec)-1] = byte(crc)
	return sec
}

func patPacket(program, pmtPID uint16, version, cc uint8) []byte {
	loop := []byte{
		byte(program >> 8), byte(program),
		0xE0 | byte(pmtPID>>8), byte(pmtPID),
	}
	sec := psiSection(0x00, 1, version, loop)
	payload := append([]byte{0x00}, sec...) // pointer field
	return tsPacket(0x0000, true, false, true, cc, payload)
}

func pmtPacket(program, audioPID, pcrPID uint16, streamType, version, cc uint8) []byte {
	loop := []byte{
		streamType,
		0xE0 | byte(audioPID>>8), byte(audioPID),
		0xF0, 0x00, // ES info length 0
	}
	inner := 9 + len(loop) + 4 // syntax(5)+PCR PID(2)+PI len(2)+loop+CRC
	sec := make([]byte, 3+inner)
	sec[0] = 0x02
	sec[1] = 0xB0 | byte(inner>>8)
	sec[2] = byte(inner)
	sec[3], sec[4] = byte(program>>8), byte(program)
	sec[5] = 0xC1 | (version&0x1F)<<1
	sec[6], sec[7] = 0, 0
	sec[8] = 0xE0 | byte(pcrPID>>8)
	sec[9] = byte(pcrPID)
	sec[10], sec[11] = 0xF0, 0x00 // program info length 0
	copy(sec[12:], loop)
	crc := crc32mpeg2(sec[:len(sec)-4])
	sec[len(sec)-4] = byte(crc >> 24)
	sec[len(sec)-3] = byte(crc >> 16)
	sec[len(sec)-2] = byte(crc >> 8)
	sec[len(sec)-1] = byte(crc)
	payload := append([]byte{0x00}, sec...)
	return tsPacket(0x1000, true, false, true, cc, payload)
}

// pesStart builds a PES-start TS packet with a PTS-only optional header.
func pesStart(pid uint16, cc uint8, pts90k int64, es []byte) []byte {
	pts := encodePTS(pts90k) // 5 bytes, per ISO 13818-1 timestamp layout
	optLen := len(pts) + 1   // flags2 + PTS field
	pesHdr := []byte{0x00, 0x00, 0x01, 0xC0}
	// PES_packet_length counts bytes after its own 2-byte field
	afterLen := 1 + 1 + optLen + len(es) // flags1 + flags2/hdrLen block + ES
	pesHdr = append(pesHdr, byte(afterLen>>8), byte(afterLen))
	pesHdr = append(pesHdr, 0x80, 0x81, byte(len(pts))) // flags1, flags2(PTS only), hdr len
	pesHdr = append(pesHdr, pts...)
	payload := append(pesHdr, es...)
	return tsPacket(pid, true, false, true, cc, payload)
}

func pesCont(pid uint16, cc uint8, es []byte) []byte {
	return tsPacket(pid, false, false, true, cc, es)
}

// pesMulti packetizes one PES across as many 188-byte TS packets as needed,
// each padded to full packet size. CCs are assigned consecutively from cc.
func pesMulti(pid uint16, cc uint8, pts90k int64, es []byte) [][]byte {
	pts := encodePTS(pts90k)
	hdrLen := len(pts)
	pesPrefix := []byte{0x00, 0x00, 0x01, 0xC0}
	declared := 3 + hdrLen + len(es) // flags1,flags2,hdrLen + optional + ES
	pesPrefix = append(pesPrefix, byte(declared>>8), byte(declared))
	pesPrefix = append(pesPrefix, 0x80, 0x81, byte(hdrLen))
	pesPrefix = append(pesPrefix, pts...)
	firstPayload := append(pesPrefix, es...)
	const maxPayload = PacketSize - 4 // header-only TS packet payload capacity
	var out [][]byte
	off := 0
	cur := cc
	for off < len(firstPayload) {
		var body []byte
		pusi := off == 0
		if len(firstPayload)-off <= maxPayload {
			body = firstPayload[off:]
		} else {
			body = firstPayload[off : off+maxPayload]
		}
		out = append(out, tsPacket(pid, pusi, false, true, cur, body))
		cur++
		off += maxPayload
	}
	return out
}

// encodePTS writes the 5-byte 33-bit timestamp field with the fixed '0001'
// marker nibble in byte 0 and the '1' marker bits at bytes 0/2/4 bit 0.
func encodePTS(ts90k int64) []byte {
	b := make([]byte, 5)
	b[0] = 0x21 | byte((ts90k>>29)&0x0E) // '0010' PTS-only marker + 3 high bits
	b[1] = byte(ts90k >> 22)
	b[2] = byte((ts90k>>14)&0xFE) | 0x01
	b[3] = byte(ts90k >> 7)
	b[4] = byte((ts90k<<1)&0xFE) | 0x01
	return b
}

func pcrPacket(pid uint16, cc uint8, pcr27m int64, discont bool) []byte {
	base := pcr27m / 300
	ext := pcr27m % 300
	pcr := make([]byte, 6)
	pcr[0] = byte(base >> 25)
	pcr[1] = byte(base >> 17)
	pcr[2] = byte(base >> 9)
	pcr[3] = byte(base >> 1)
	pcr[4] = byte(base&0x01)<<7 | byte(ext>>8)
	pcr[5] = byte(ext)
	flags := byte(0x10) // PCR flag
	if discont {
		flags |= 0x80
	}
	// adaptation field body begins at pkt[4]: length, flags, then 6 PCR bytes
	af := append([]byte{7, flags}, pcr...)
	return tsPacket(pid, false, true, false, cc, af)
}

// collect runs a demuxer over concatenated packets and gathers events.
func collect(packets [][]byte, policy Policy) ([]TimelineEvent, Stats, error) {
	var buf []byte
	for _, p := range packets {
		buf = append(buf, p...)
	}
	d := NewDemuxer(bytesReader(buf), policy)
	var evs []TimelineEvent
	st, err := d.Run(backgroundCtx(), func(e TimelineEvent) { evs = append(evs, e) })
	return evs, st, err
}

func isCode(err error, code clock.Code) bool {
	e := clock.AsError(err)
	return e != nil && e.Code == code
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func backgroundCtx() context.Context { return context.Background() }
