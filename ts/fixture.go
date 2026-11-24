package ts

import (
	"encoding/binary"
)

// BuildPacket assembles one 188-byte TS packet from the given fields.
// pcr27 >= 0 embeds a PCR in the adaptation field; discont sets the
// adaptation-field discontinuity indicator. payload may be nil.
func BuildPacket(pid uint16, cc uint8, pusi bool, payload []byte, pcr27 int64, discont bool) []byte {
	b := make([]byte, Size188)
	b[0] = SyncByte
	if pusi {
		b[1] |= 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	needAdapt := pcr27 >= 0 || discont
	flags := byte(0x10) // payload only
	if needAdapt {
		flags |= 0x20
		if payload == nil {
			flags = 0x20 // adaptation only
		}
	}
	b[3] = flags | (cc & 0x0F)
	pos := 4
	if needAdapt {
		var field []byte
		if pcr27 >= 0 {
			field = make([]byte, 7)
			field[0] = adaptPCR
			base, ext := pcr27/300, pcr27%300
			field[1] = byte(base >> 25)
			field[2] = byte(base >> 17)
			field[3] = byte(base >> 9)
			field[4] = byte(base >> 1)
			field[5] = byte(base&1)<<7 | byte(ext>>8)
			field[6] = byte(ext)
			if discont {
				field[0] |= adaptDiscont
			}
		} else if discont {
			field = []byte{adaptDiscont}
		}
		b[pos] = byte(len(field))
		copy(b[pos+1:], field)
		pos += 1 + len(field)
	}
	if payload != nil {
		copy(b[pos:], payload)
	}
	return b
}

// BuildPESPayload wraps a small synthetic PES payload carrying a 33-bit PTS.
func BuildPESPayload(pts90 uint64, fill int) []byte {
	payload := make([]byte, 14+fill)
	payload[0], payload[1], payload[2] = 0x00, 0x00, 0x01
	payload[3] = 0xE0 // video stream id
	payload[7] = 0x80 // PTS_DTS_flags = 0b10
	payload[8] = 5    // PES header data length
	payload[9] = 0x21
	payload[9] |= byte(pts90>>29) & 0x0E
	payload[10] = byte(pts90 >> 22)
	payload[11] = byte(pts90>>14) | 0x01
	payload[12] = byte(pts90 >> 7)
	payload[13] = byte(pts90<<1) | 0x01
	return payload
}

// BuildPAT emits a minimal PAT section listing one PMT PID.
func BuildPAT(pmtPID uint16, version uint8) []byte {
	// Body: 8 bytes fixed PAT header, 4 bytes program entry, 4 bytes zeroed
	// CRC32 placeholder. section_length excludes table_id and its own 2 bytes.
	body := make([]byte, 8+4+4)
	body[0] = 0x00 // table_id PAT
	binary.BigEndian.PutUint16(body[1:3], 0xB000|uint16(len(body)-3))
	body[3] = 0x01 // transport_stream_id hi
	body[4] = 0x01 // transport_stream_id lo
	body[5] = 0xC1 | (version&0x1F)<<1
	body[6] = 0x00 // section_number
	body[7] = 0x00 // last_section_number
	binary.BigEndian.PutUint16(body[8:10], 1)
	binary.BigEndian.PutUint16(body[10:12], 0xE000|pmtPID)
	return withPointerAndCRC(body)
}

// BuildPMT emits a PMT section with one audio and one video elementary stream.
func BuildPMT(pmtPID, audioPID, videoPID uint16, version uint8, meta TrackMeta, color string) []byte {
	audioDesc := []byte{DescAudio, 6, meta.Channels, meta.Layout,
		byte(meta.SampleRate >> 24), byte(meta.SampleRate >> 16),
		byte(meta.SampleRate >> 8), byte(meta.SampleRate)}
	colorBody := append([]byte{byte(len(color))}, []byte(color)...)
	colorDesc := append([]byte{DescColor, byte(len(colorBody))}, colorBody...)
	esInfo := append(append([]byte{}, audioDesc...), colorDesc...)
	videoInfo := colorDesc
	// Body layout: 12 bytes of fixed PMT header, both ES loops, then a
	// 4-byte zeroed CRC32 placeholder. section_length excludes the table_id
	// and its own 2 bytes, i.e. len(body)-3.
	body := make([]byte, 12+5+len(esInfo)+5+len(videoInfo)+4)
	sectionLen := len(body) - 3
	binary.BigEndian.PutUint16(body[1:3], 0xB000|uint16(sectionLen))
	body[0] = 0x02 // PMT
	body[5] = 0xC1 | (version&0x1F)<<1
	binary.BigEndian.PutUint16(body[8:10], 1) // program number
	binary.BigEndian.PutUint16(body[10:12], 0xF000)
	pos := 12
	putES := func(streamType, pid uint16, info []byte) {
		body[pos] = byte(streamType)
		binary.BigEndian.PutUint16(body[pos+1:pos+3], 0xE000|pid)
		binary.BigEndian.PutUint16(body[pos+3:pos+5], 0xF000|uint16(len(info)))
		copy(body[pos+5:], info)
		pos += 5 + len(info)
	}
	putES(0x0F, audioPID, esInfo)
	putES(0x1B, videoPID, videoInfo)
	return withPointerAndCRC(body)
}

// withPointerAndCRC prepends the pointer_field. Sections already include a
// 4-byte zeroed CRC32 placeholder; fixtures are validated structurally, not
// by checksum.
func withPointerAndCRC(section []byte) []byte {
	out := make([]byte, 1+len(section))
	out[0] = 0 // pointer_field
	copy(out[1:], section)
	return out
}
