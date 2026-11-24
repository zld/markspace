package fixture

// BuildTS returns a small single-program MPEG-TS stream carrying an H.264
// video PID with an SEI message, an AAC audio PID and an ID3 metadata PID.
// The packet layout is deterministic so continuity errors can be injected at a
// known byte offset.
func BuildTS() Payload {
	pmtPID := uint16(0x1000)
	videoPID := uint16(0x0100)
	audioPID := uint16(0x0101)
	id3PID := uint16(0x0102)

	var out wbuf
	cc := map[uint16]byte{}
	// writePacket pads with an adaptation field rather than zero bytes, so
	// PES payload lengths stay exact and sample sizes are stable.
	writePacket := func(pid uint16, pusi bool, payload []byte) {
		if len(payload) > tsPacketSize-4 {
			panic("fixture: PES does not fit one transport packet")
		}
		pkt := make([]byte, tsPacketSize)
		pkt[0] = 0x47
		pkt[1] = byte(pid >> 8 & 0x1f)
		if pusi {
			pkt[1] |= 0x40
		}
		pkt[2] = byte(pid)
		start := 4
		if len(payload) < tsPacketSize-4 {
			pkt[3] = 0x30 | cc[pid]&0x0f // adaptation field + payload
			stuffing := tsPacketSize - 4 - len(payload)
			pkt[4] = byte(stuffing - 1)
			if stuffing > 1 {
				pkt[5] = 0x00 // flags
				for i := 6; i < 4+stuffing; i++ {
					pkt[i] = 0xff
				}
			}
			start = 4 + stuffing
		} else {
			pkt[3] = 0x10 | cc[pid]&0x0f
		}
		cc[pid]++
		copy(pkt[start:], payload)
		out.raw(pkt)
	}

	patSection := psiSection(0x00, 0x0001, []byte{
		0x00, 0x01, byte(pmtPID >> 8), byte(pmtPID),
	})
	writePacket(0, true, append([]byte{0x00}, patSection...))

	pmtBody := []byte{
		byte(videoPID >> 8), byte(videoPID), 0xf0, 0x00,
		byte(audioPID >> 8), byte(audioPID), 0xf0, 0x00,
		byte(id3PID >> 8), byte(id3PID), 0xf0, 0x00,
	}
	pmtSection := psiSectionMult(0x02, 0x0001, 0x0001, pmtBody, []byte{
		0x1b, // H.264
		0x0f, // AAC ADTS
		0x15, // ID3
	})
	writePacket(pmtPID, true, append([]byte{0x00}, pmtSection...))

	pes := func(streamID byte, pts uint64, payload []byte) []byte {
		hdrLen := 5
		hdr := []byte{0x00, 0x00, 0x01, streamID, 0x00, 0x00, 0x80, 0x80, byte(hdrLen)}
		hdr = append(hdr, encodePTS(pts)...)
		// PES_packet_length covers everything after these 6 bytes.
		length := 3 + hdrLen + len(payload)
		hdr[4] = byte(length >> 8)
		hdr[5] = byte(length)
		return append(hdr, payload...)
	}
	// One user_data_unregistered SEI: type 5 with a 16-byte UUID followed by
	// application data, then rbsp_trailing_bits.
	seiMsg := append([]byte("0123456789abcdef"), []byte("ts-meta")...)
	videoSEI := []byte{0x00, 0x00, 0x00, 0x01, 0x06, 0x05, byte(len(seiMsg))}
	videoSEI = append(videoSEI, seiMsg...)
	videoSEI = append(videoSEI, 0x80)
	audio := []byte{0xff, 0xf1, 0x50, 0x80, 0x00, 0x1f, 0xfc}
	id3 := buildID3("TIT2", []byte("ts"))

	writePacket(videoPID, true, pes(0xe0, 90000, videoSEI))
	writePacket(audioPID, true, pes(0xc0, 48000, audio))
	writePacket(id3PID, true, pes(0xbd, 0, id3)) // private_stream_1 carries ID3
	return Payload{Name: "valid.ts", Data: out.bytes()}
}

const tsPacketSize = 188

// psiSection builds a short PSI section with a one-entry body.
func psiSection(tableID byte, extension uint16, body []byte) []byte {
	return psiSectionMult(tableID, extension, 0x0001, body, nil)
}

// psiSectionMult assembles a PSI section and computes its CRC-32/MPEG-2.
func psiSectionMult(tableID byte, extension uint16, version uint8, body []byte, streamTypes []byte) []byte {
	var b wbuf
	b.u8(tableID)
	length := 5 + len(body) + 4
	b.u16(uint16(0xb000 | length))
	b.u16(extension)
	b.u8(byte(0xc1 | (version&0x1f)<<1))
	b.u8(0)
	b.u8(0)
	if tableID == 0x02 {
		// program_info_length then the elementary streams
		b.u16(uint16(0xf000))
		for i := 0; i+4 <= len(body); i += 4 {
			b.u8(streamTypes[i/4])
			b.raw(body[i : i+4])
		}
	} else {
		b.raw(body)
	}
	section := b.bytes()
	crc := crc32MPEG(section)
	var t [4]byte
	t[0] = byte(crc >> 24)
	t[1] = byte(crc >> 16)
	t[2] = byte(crc >> 8)
	t[3] = byte(crc)
	return append(section, t[:]...)
}

func crc32MPEG(data []byte) uint32 {
	var crc uint32 = 0xffffffff
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func encodePTS(pts uint64) []byte {
	return []byte{
		byte(0x21 | ((pts >> 29) & 0x0e)),
		byte(pts >> 22),
		byte(0x01 | ((pts >> 14) & 0xfe)),
		byte(pts >> 7),
		byte(0x01 | ((pts << 1) & 0xfe)),
	}
}
