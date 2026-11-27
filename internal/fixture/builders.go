package fixture

import (
	"tsrepair/internal/ts"
)

// crc32MPEG2 mirrors the TS package's section CRC for fixture generation.
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

func appendCRC(section []byte) []byte {
	crc := crc32MPEG2(section[:len(section)-4])
	section[len(section)-4] = byte(crc >> 24)
	section[len(section)-3] = byte(crc >> 16)
	section[len(section)-2] = byte(crc >> 8)
	section[len(section)-1] = byte(crc)
	return section
}

// buildPAT constructs a one-section PAT for the configured PMT PID.
func (b *builder) buildPAT(version byte) []byte {
	section := []byte{
		ts.TableIDPAT,
		0xB0, 0x0D, // section_length = 13
		0x00, 0x01, // transport_stream_id
		0xC1 | (version << 1),
		0x00, 0x00, // section 0 of 0
		0x00, 0x01, // program number 1
		0xE0 | byte(b.opts.PMTPID>>8), byte(b.opts.PMTPID),
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
	}
	return appendCRC(section)
}

// buildPMT constructs a one-section PMT naming the video PID.
func (b *builder) buildPMT() []byte {
	body := []byte{
		ts.TableIDPMT,
		0xB0, 0x00, // length placeholder
		0x00, 0x01, // program number
		0xC1, 0x00, 0x00, // version 0, sections 0/0
		0xE0 | byte(b.opts.PCRPID>>8), byte(b.opts.PCRPID),
		0xF0, 0x00, // program info length 0
		0x1B, // H.264 video stream type
		0xE0 | byte(b.opts.VideoPID>>8), byte(b.opts.VideoPID),
		0xF0, 0x00, // ES info length 0
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
	}
	n := len(body) - 3
	body[1] = 0xB0 | byte(n>>8)
	body[2] = byte(n)
	return appendCRC(body)
}

// emitSection wraps a PSI section in a PUSI TS packet (pointer field 0).
func (b *builder) emitSection(pid uint16, section []byte) {
	payload := append([]byte{0x00}, section...)
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	pkt[1] = 0x40 | byte(pid>>8) // PUSI
	pkt[2] = byte(pid)
	pkt[3] = byte(ts.AFCPayloadOnly)<<4 | b.nextCC(pid)
	copy(pkt[4:], payload)
	for i := 4 + len(payload); i < ts.PacketSize; i++ {
		pkt[i] = 0xFF
	}
	b.emit(pkt)
}

// emitPCRPacket writes an adaptation+payload packet with a PCR.
func (b *builder) emitPCRPacket(pid uint16, rawPCR int64, keyframe bool) {
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	pkt[1] = byte(pid >> 8)
	pkt[2] = byte(pid)
	pkt[3] = byte(ts.AFCPayloadAndAF)<<4 | b.nextCC(pid)
	pkt[4] = 7          // adaptation_field_length
	flags := byte(0x10) // PCR flag
	if keyframe {
		flags |= 0x40 // random_access_indicator
	}
	pkt[5] = flags
	base := rawPCR / 300
	ext := rawPCR % 300
	pkt[6] = byte(base >> 25)
	pkt[7] = byte(base >> 17)
	pkt[8] = byte(base >> 9)
	pkt[9] = byte(base >> 1)
	pkt[10] = byte(base&1)<<7 | byte(ext>>8)
	pkt[11] = byte(ext)
	for i := 12; i < ts.PacketSize; i++ {
		pkt[i] = 0xFF
	}
	b.emit(pkt)
}

// buildPES constructs a minimal video PES with a PTS and an IDR NAL when the
// sample is a keyframe.
func (b *builder) buildPES(pts90k int64, keyframe bool) []byte {
	var nal []byte
	if keyframe {
		nal = []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88} // AUD + IDR
	} else {
		nal = []byte{0x00, 0x00, 0x00, 0x01, 0x61, 0x88} // non-IDR slice
	}
	ptsBytes := encodePTS(0x21, pts90k)
	hdrLen := byte(5)
	head := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, hdrLen}
	pes := append(head, ptsBytes...)
	pes = append(pes, nal...)
	n := len(pes) - 6
	pes[4] = byte(n >> 8)
	pes[5] = byte(n)
	return pes
}

// emitPESPacket places a PES packet in a PUSI TS payload, splitting across
// continuation packets when it does not fit in one.
func (b *builder) emitPESPacket(pid uint16, pes []byte, keyframe bool) {
	first := true
	for len(pes) > 0 {
		pkt := make([]byte, ts.PacketSize)
		pkt[0] = ts.SyncByte
		pkt[1] = byte(pid >> 8)
		if first {
			pkt[1] |= 0x40 // PUSI
		}
		pkt[2] = byte(pid)
		pkt[3] = byte(ts.AFCPayloadOnly)<<4 | b.nextCC(pid)
		space := ts.PacketSize - 4
		n := len(pes)
		if n > space {
			n = space
		}
		copy(pkt[4:], pes[:n])
		for i := 4 + n; i < ts.PacketSize; i++ {
			pkt[i] = 0xFF
		}
		b.emit(pkt)
		pes = pes[n:]
		first = false
	}
}

// encodePTS packs a 33-bit PTS into the 5-byte MPEG layout.
func encodePTS(prefix byte, pts int64) []byte {
	return []byte{
		prefix | byte(pts>>29)&0x0E | 0x01,
		byte(pts >> 22),
		byte(pts>>14)&0xFE | 0x01,
		byte(pts >> 7),
		byte(pts<<1)&0xFE | 0x01,
	}
}
