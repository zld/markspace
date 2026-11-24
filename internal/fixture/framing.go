// Package fixture generates self-contained MPEG-TS/RTP sample streams for the
// acceptance scenarios. No external media is required: every byte is produced
// deterministically in code.
package fixture

import (
	"encoding/binary"
	"math"

	ts "mediarun/internal/ts"
)

// Well-known PIDs used by every generated program.
const (
	PatPID   uint16 = 0x0000
	PMTPID   uint16 = 0x0010
	AudioPID uint16 = 0x0100
	VideoPID uint16 = 0x0200
	PCRPID   uint16 = 0x0100 // PCR rides on the audio PID adaptation field
)

// Scenario names accepted by the CLI and engine.
const (
	KindNormal    = "normal"
	KindPCRWrap   = "pcr_wrap"
	KindCCJump    = "cc_jump"
	KindPSIUpdate = "psi_update"
	KindLoss      = "loss" // RTP transport with reorder/loss/duplicates
	KindTruncated = "truncated"
	KindDamaged   = "damaged" // combined raw-TS damage for the run command
)

// AllKinds lists every generated scenario in stable order.
func AllKinds() []string {
	return []string{KindNormal, KindPCRWrap, KindCCJump, KindPSIUpdate, KindLoss, KindTruncated, KindDamaged}
}

// AudioFrameMarker opens each in-band audio access unit carried in TS payloads.
// The documented framing is: marker(1) | segment(2 BE) | offset(4 BE) | wav bytes.
const AudioFrameMarker byte = 0xA5

const frameHeaderLen = 7

// tsBuilder emits 188-byte packets while tracking each PID's continuity counter.
type tsBuilder struct {
	out []byte
	cc  map[uint16]uint8
}

func newTSBuilder() *tsBuilder { return &tsBuilder{cc: map[uint16]uint8{}} }

func (b *tsBuilder) nextCC(pid uint16) uint8 {
	c := b.cc[pid]
	b.cc[pid] = (c + 1) & 0x0f
	return c
}

// rawPacket constructs one packet with full control over header fields.
func rawPacket(pid uint16, cc uint8, pusi bool, afc uint8, adaptation, payload []byte) []byte {
	buf := make([]byte, ts.PacketSize)
	buf[0] = ts.SyncByte
	if pusi {
		buf[1] |= 0x40
	}
	buf[1] |= byte(pid >> 8)
	buf[2] = byte(pid)
	buf[3] = (afc << 4) | (cc & 0x0f)
	cursor := 4
	if afc == 2 || afc == 3 {
		buf[4] = byte(len(adaptation))
		copy(buf[5:], adaptation)
		cursor = 5 + len(adaptation)
	}
	copy(buf[cursor:], payload)
	return buf
}

// pcrAdaptation builds an adaptation field body (flags byte + 6 PCR bytes).
func pcrAdaptation(pcrBase uint64) []byte {
	body := make([]byte, 7)
	body[0] = 0x10 // PCR flag
	body[1] = byte(pcrBase >> 25)
	body[2] = byte(pcrBase >> 17)
	body[3] = byte(pcrBase >> 9)
	body[4] = byte(pcrBase >> 1)
	body[5] = 0
	body[6] = 0
	return body
}

// audioPackets splits one segment's WAV bytes into framed TS payload packets.
// pcrBase is attached to the first packet's adaptation field.
func (b *tsBuilder) audioPackets(segment int, wav []byte, pcrBase uint64) [][]byte {
	const mtu = ts.PacketSize - 4 // payload-only packets
	var pkts [][]byte
	for off := 0; off < len(wav); {
		room := mtu - frameHeaderLen
		chunk := wav[off:]
		if len(chunk) > room {
			chunk = chunk[:room]
		}
		payload := make([]byte, 0, frameHeaderLen+len(chunk))
		payload = append(payload, AudioFrameMarker)
		payload = binary.BigEndian.AppendUint16(payload, uint16(segment))
		payload = binary.BigEndian.AppendUint32(payload, uint32(off))
		payload = append(payload, chunk...)
		cc := b.nextCC(AudioPID)
		if off == 0 {
			adapt := pcrAdaptation(pcrBase)
			pkts = append(pkts, rawPacket(AudioPID, cc, true, 0x03, adapt, payload))
		} else {
			pkts = append(pkts, rawPacket(AudioPID, cc, false, 0x01, nil, payload))
		}
		off += len(chunk)
	}
	return pkts
}

// makeSegmentWAV builds a short deterministic PCM WAV for one 1-second timeline
// segment. Odd segments are mono 24 kHz, even ones stereo 48 kHz, so the
// normalizer genuinely exercises resampling and channel mapping.
func makeSegmentWAV(segment int) []byte {
	mono := segment%2 == 1
	var rate uint32 = 48000
	channels := uint16(2)
	if mono {
		rate, channels = 24000, 1
	}
	seconds := 1.0 // each segment is one timeline second
	frames := int(float64(rate) * seconds)
	bits := uint16(16)
	dataSize := uint32(frames * int(channels) * int(bits) / 8)
	buf := make([]byte, 44+int(dataSize))
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], 36+dataSize)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], channels)
	binary.LittleEndian.PutUint32(buf[24:28], rate)
	binary.LittleEndian.PutUint32(buf[28:32], rate*uint32(channels)*uint32(bits)/8)
	binary.LittleEndian.PutUint16(buf[32:34], channels*bits/8)
	binary.LittleEndian.PutUint16(buf[34:36], bits)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataSize)
	// Distinct fundamental per segment keeps content digests different.
	freq := 220.0 + float64(segment)*40.0
	for i := 0; i < frames; i++ {
		for c := 0; c < int(channels); c++ {
			v := 0.8*math.Sin(2*math.Pi*freq*float64(i)/float64(rate)) +
				0.2*math.Sin(2*math.Pi*freq*2*float64(i)/float64(rate))
			sample := int16(0.3 * float64(math.MaxInt16) * v)
			pos := 44 + (i*int(channels)+c)*2
			binary.LittleEndian.PutUint16(buf[pos:], uint16(sample))
		}
	}
	return buf
}
