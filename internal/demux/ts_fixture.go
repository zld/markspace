package demux

import "mediarc/internal/model"

// TSFrame is one generated elementary frame for the MPEG-TS fixture.
type TSFrame struct {
	PTS, DTS uint64
	Payload  []byte
	Key      bool
	// Discontinuity sets the TS adaptation-field discontinuity indicator on
	// the packet carrying this frame.
	Discontinuity bool
}

// WriteMPEGTS builds a minimal standards-shaped MPEG-TS carrying one video
// elementary stream (PID 0x0100) and, when audio frames are present, one
// audio stream (PID 0x0102). PAT/PMT are emitted first. The PMT keeps its own
// PID so it can never collide with an elementary stream.
func WriteMPEGTS(video, audio []TSFrame) []byte {
	const pmtPID, videoPID, audioPID = 0x1000, 0x0100, 0x0102
	var out []byte
	out = append(out, makePATPacket(pmtPID)...)
	out = append(out, makePMTPacket(pmtPID, videoPID, audioPID, len(audio) > 0)...)
	// Continuity counters are per PID and must keep incrementing across PES
	// packet boundaries for the whole fragment.
	cc := map[uint16]uint8{videoPID: 0, audioPID: 0}
	for _, f := range video {
		for _, p := range packetizePES(videoPID, 0x1B, f, cc) {
			out = append(out, p...)
		}
	}
	for _, f := range audio {
		for _, p := range packetizePES(audioPID, 0x0F, f, cc) {
			out = append(out, p...)
		}
	}
	return out
}

func makeTSPacket(pusi bool, pid uint16, cc uint8, payload []byte, disco bool) []byte {
	pkt := make([]byte, tsPacketSize)
	pkt[0] = tsSyncByte
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[1] |= byte(pid >> 8)
	pkt[2] = byte(pid)
	pos := 4
	if disco {
		// One-byte adaptation field carrying only the discontinuity flag.
		pkt[3] = 0x20 | 0x10 | cc
		pkt[4] = 1
		pkt[5] = 0x80
		pos = 6
	} else {
		pkt[3] = 0x10 | cc
	}
	copy(pkt[pos:], payload)
	for i := pos + len(payload); i < tsPacketSize; i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

func makePATPacket(pmtPID uint16) []byte {
	payload := make([]byte, 184)
	payload[0] = 0 // pointer field
	sec := payload[1:]
	sec[0] = 0x00 // table id PAT
	// section length 13: fixed header(5)+program(4)+CRC(4)
	sec[1], sec[2] = 0xB0, 13
	sec[3], sec[4] = 0x00, 0x01 // transport stream id
	sec[5] = 0xC1               // version 0, current/next
	sec[6], sec[7] = 0x00, 0x00 // section/last section number
	sec[8], sec[9] = 0x00, 0x01 // program 1
	sec[10], sec[11] = byte(0xE0|pmtPID>>8), byte(pmtPID)
	for i := 1 + 3 + 13; i < 184; i++ {
		payload[i] = 0xFF
	}
	return makeTSPacket(true, pidPAT, 0, payload, false)
}

func makePMTPacket(pmtPID, videoPID, audioPID uint16, withAudio bool) []byte {
	payload := make([]byte, 184)
	payload[0] = 0
	sec := payload[1:]
	sec[0] = 0x02 // PMT table id
	esCount := 1
	if withAudio {
		esCount++
	}
	// section_length covers everything after the length field: fixed PMT
	// fields (9 bytes incl. CRC placeholder), 5 bytes per ES, and the CRC32.
	sectionLen := 9 + 5*esCount + 4
	sec[1], sec[2] = 0xB0, byte(sectionLen)
	sec[3], sec[4] = 0x00, 0x01
	sec[5] = 0xC1
	sec[6], sec[7] = byte(videoPID>>8&0x1F), byte(videoPID) // PCR PID
	sec[8], sec[9] = 0xF0, 0x00                             // zero program info length
	p := 10
	sec[p], sec[p+1], sec[p+2] = 0x1B, byte(0xE0|videoPID>>8), byte(videoPID)
	sec[p+3], sec[p+4] = 0xF0, 0x00 // zero ES info length
	p += 5
	if withAudio {
		sec[p], sec[p+1], sec[p+2] = 0x0F, byte(0xE0|audioPID>>8), byte(audioPID)
		sec[p+3], sec[p+4] = 0xF0, 0x00
	}
	for i := 1 + 3 + sectionLen; i < 184; i++ {
		payload[i] = 0xFF
	}
	return makeTSPacket(true, pmtPID, 0, payload, false)
}

func packetizePES(pid uint16, streamType byte, f TSFrame, cc map[uint16]uint8) [][]byte {
	pes := buildPES(streamIDForType(streamType), f)
	var pkts [][]byte
	first := true
	for len(pes) > 0 {
		room := 184
		if first && f.Discontinuity {
			room = 188 - 6 // two adaptation-field header bytes occupy the space
		}
		n := len(pes)
		if n > room {
			n = room
		}
		pkts = append(pkts, makeTSPacket(first, pid, cc[pid], pes[:n], first && f.Discontinuity))
		pes = pes[n:]
		cc[pid] = (cc[pid] + 1) & 0x0F
		first = false
	}
	return pkts
}

func streamIDForType(streamType byte) byte {
	if streamType == 0x0F {
		return 0xC0 // audio stream id
	}
	return 0xE0 // video stream id
}

func buildPES(streamID byte, f TSFrame) []byte {
	hasDTS := f.DTS != f.PTS
	headerDataLen := byte(5) // PTS only
	if hasDTS {
		headerDataLen = 10
	}
	flags := byte(0x80) // PTS present
	if hasDTS {
		flags |= 0x40
	}
	pes := []byte{0x00, 0x00, 0x01, streamID, 0x00, 0x00, 0x80, flags, headerDataLen}
	pes = append(pes, encodeTimestamp(0x20, f.PTS)...)
	if hasDTS {
		pes = append(pes, encodeTimestamp(0x10, f.DTS)...)
	}
	pes = append(pes, f.Payload...)
	// Patch PES packet length (everything after the two length bytes).
	pesLen := len(pes) - 6
	pes[4] = byte(pesLen >> 8)
	pes[5] = byte(pesLen)
	return pes
}

// encodeTimestamp encodes a 33-bit PTS/DTS into the five marker-bit fields
// per ISO 13818-1 2.5.3.6: 3/15/15 bits with fixed marker 1-bits.
func encodeTimestamp(prefix byte, ts uint64) []byte {
	return []byte{
		prefix | byte(ts>>30&0x07)<<1 | 0x01, // '0010' + ts[32..30] + marker
		byte(ts >> 22),                       // ts[29..22]
		byte(ts>>15&0x7F)<<1 | 0x01,          // ts[21..15] + marker
		byte(ts >> 7),                        // ts[14..7]
		byte(ts&0x7F)<<1 | 0x01,              // ts[6..0] + marker
	}
} // TSFramesToTrack mirrors generated frames into a model.Track for tests that
// bypass the parser.
func TSFramesToTrack(id uint32, frames []TSFrame) model.Track {
	tr := model.Track{ID: id, Codec: model.CodecAVC1, Timescale: tsClockRate}
	for _, f := range frames {
		tr.Samples = append(tr.Samples, model.Sample{
			Dts: f.DTS, Payload: append([]byte(nil), f.Payload...), Sync: f.Key,
		})
	}
	return tr
}
