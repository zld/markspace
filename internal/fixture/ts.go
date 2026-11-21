// Package fixture generates deterministic media segments and case
// directories for local verification. It writes real MPEG-TS files with an
// AAC elementary stream so that ffprobe reports exact durations, without
// invoking any external encoder.
package fixture

import (
	"fmt"
)

// AAC frame duration is 1024 samples at 44.1 kHz.
const (
	audioSampleRate = 44100
	aacFrameSamples = 1024
	tsTimescale     = 90000
	tsPacketSize    = 188
	nullPID         = 0x1FFF
)

// crc32MPEG implements the MPEG-2 CRC used by PSI sections.
func crc32MPEG(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func psiSection(tableID byte, body []byte) []byte {
	sectionLength := len(body) + 5 + 4
	out := []byte{tableID, byte(0xB0 | sectionLength>>8), byte(sectionLength)}
	out = append(out, 0x00, 0x01, 0xC1, 0x00, 0x00)
	out = append(out, body...)
	crc := crc32MPEG(out)
	return append(out, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func patSection() []byte {
	body := []byte{0x00, 0x01, 0xE0, 0x00 | 0x10, 0x00} // program 1 -> PMT PID 0x1000
	return psiSection(0x00, body)
}

func pmtSection() []byte {
	body := []byte{0xE1, 0x00, 0xF0, 0x00} // PCR PID 0x100
	body = append(body, 0x0F, 0xE1, 0x01, 0xF0, 0x00)
	return psiSection(0x02, body)
}

func ptsField(pts uint64) []byte {
	return []byte{
		0x21 | byte((pts>>29)&0x0E),
		byte(pts >> 22),
		0x01 | byte((pts>>14)&0xFE),
		byte(pts >> 7),
		0x01 | byte((pts<<1)&0xFE),
	}
}

func pesPacket(streamID byte, pts uint64, payload []byte) []byte {
	body := []byte{0x80, 0x80, 0x05}
	body = append(body, ptsField(pts)...)
	body = append(body, payload...)
	head := []byte{0x00, 0x00, 0x01, streamID, byte(len(body) >> 8), byte(len(body))}
	return append(head, body...)
}

// adtsFrame builds one AAC-LC frame with an END element (0xE0) and no raw
// data, so decoders see a complete, valid access unit.
func adtsFrame() []byte {
	const channelConfig = 1 // mono
	raw := []byte{0xE0}
	n := len(raw) + 7
	return []byte{
		0xFF, 0xF1,
		(1 << 6) | (4 << 2) | (channelConfig >> 2),
		((channelConfig & 3) << 6) | byte((n>>11)&3),
		byte(n >> 3),
		byte((n&7)<<5) | 0x1F,
		0xFC,
		raw[0],
	}
}

func tsPacket(pid uint16, cc byte, payload []byte, pusi bool, pcr *uint64) []byte {
	b0 := byte(0x47)
	b1 := byte((pid >> 8) & 0x1F)
	if pusi {
		b1 |= 0x40
	}
	afc := byte(0x10)
	var adaptation []byte
	if pcr != nil {
		afc = 0x30
		base := *pcr & ((1 << 33) - 1)
		adaptation = []byte{0x10,
			byte(base >> 25), byte(base >> 17), byte(base >> 9), byte(base >> 1),
			byte((base&1)<<7) | 0x7E, 0x00}
		adaptation = append([]byte{byte(len(adaptation))}, adaptation...)
	}
	out := []byte{b0, b1, byte(pid), afc | (cc & 0x0F)}
	out = append(out, adaptation...)
	out = append(out, payload...)
	if pad := tsPacketSize - len(out); pad > 0 {
		out = append(out, make([]byte, pad)...)
	}
	return out
}

func nullPackets(count int) []byte {
	out := make([]byte, 0, count*tsPacketSize)
	for i := 0; i < count; i++ {
		pkt := make([]byte, tsPacketSize)
		pkt[0] = 0x47
		pkt[1] = 0x1F
		pkt[2] = 0xFF
		pkt[3] = 0x10
		for j := 4; j < tsPacketSize; j++ {
			pkt[j] = 0xFF
		}
		out = append(out, pkt...)
	}
	return out
}

// TS builds an MPEG-TS segment with exactly durationMS of timeline, padded
// with null packets so that higher bitrates produce larger files.
func TS(durationMS int64, paddingBytes int) ([]byte, error) {
	if durationMS <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}
	if paddingBytes < 0 || paddingBytes%(tsPacketSize-4) != 0 {
		return nil, fmt.Errorf("padding must be a non-negative multiple of %d", tsPacketSize-4)
	}
	totalTicks := uint64((durationMS*tsTimescale + 500) / 1000)
	frames := int((durationMS*audioSampleRate + 512) / 1000 / aacFrameSamples)
	if frames < 1 {
		frames = 1
	}
	out := tsPacket(0, 0, append([]byte{0x00}, patSection()...), true, nil)
	out = append(out, tsPacket(0x1000, 0, append([]byte{0x00}, pmtSection()...), true, nil)...)
	frame := adtsFrame()
	for i := 0; i < frames; i++ {
		var pts uint64
		if frames > 1 {
			pts = uint64(i) * totalTicks / uint64(frames-1)
		}
		out = append(out, tsPacket(0x0101, byte(i), pesPacket(0xC0, pts, frame), true, &pts)...)
	}
	out = append(out, nullPackets(paddingBytes/(tsPacketSize-4))...)
	return out, nil
}
