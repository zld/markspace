// Package fixtures deterministically synthesizes the sample data used by the
// demo command and the module tests. Nothing is read from external files.
package fixtures

import (
	"encoding/binary"
	"math"

	"github.com/pion/rtp"
)

// WAVOptions controls the synthetic PCM WAV layout.
type WAVOptions struct {
	SampleRate  int
	Channels    int
	Bits        int
	Frames      int
	Seed        uint64
	BlockFrames int // block that ModifyBlock refers to
	ModifyBlock int // -1 keeps the samples untouched
	ExtraChunk  string
	ExtraBytes  int
	Unaligned   int // extra bytes appended to the data chunk size
	Truncate    int // bytes cut from the end after building
}

// WAV renders a complete RIFF/WAVE file.
func WAV(o WAVOptions) []byte {
	if o.SampleRate == 0 {
		o.SampleRate = 8000
	}
	if o.Channels == 0 {
		o.Channels = 1
	}
	if o.Bits == 0 {
		o.Bits = 16
	}
	if o.Frames == 0 {
		o.Frames = 16384
	}
	if o.BlockFrames == 0 {
		o.BlockFrames = 512
	}
	frameBytes := o.Channels * o.Bits / 8
	pcm := make([]byte, o.Frames*frameBytes)
	rng := o.Seed*6364136223846793005 + 1442695040888963407
	next := func() uint32 {
		rng = rng*6364136223846793005 + 1442695040888963407
		return uint32(rng >> 32)
	}
	for f := 0; f < o.Frames; f++ {
		for c := 0; c < o.Channels; c++ {
			v := next()
			off := (f*o.Channels + c) * (o.Bits / 8)
			writeSample(pcm[off:], v, o.Bits)
		}
	}
	if o.ModifyBlock >= 0 && o.BlockFrames > 0 {
		start := o.ModifyBlock * o.BlockFrames * frameBytes
		if start < len(pcm) {
			end := start + o.BlockFrames*frameBytes
			if end > len(pcm) {
				end = len(pcm)
			}
			for i := start; i < end; i++ {
				pcm[i] ^= 0xA5
			}
		}
	}

	var body []byte
	body = append(body, chunk("fmt ", fmtPayload(o.SampleRate, o.Channels, o.Bits, frameBytes))...)
	if o.ExtraChunk != "" && o.ExtraBytes > 0 {
		extra := make([]byte, o.ExtraBytes)
		for i := range extra {
			extra[i] = byte(i*7 + o.ExtraBytes)
		}
		body = append(body, chunk(o.ExtraChunk, extra)...)
	}
	data := make([]byte, len(pcm)+o.Unaligned)
	copy(data, pcm)
	for i := len(pcm); i < len(data); i++ {
		data[i] = 0xC3
	}
	body = append(body, chunk("data", data)...)

	out := make([]byte, 0, 12+len(body))
	out = append(out, []byte("RIFF")...)
	out = append(out, u32le(uint32(4+len(body)))...)
	out = append(out, []byte("WAVE")...)
	out = append(out, body...)
	if o.Truncate > 0 {
		if o.Truncate >= len(out) {
			return out[:0]
		}
		out = out[:len(out)-o.Truncate]
	}
	return out
}

// ContainerVariant returns a WAV carrying the same PCM with an extra LIST chunk
// before the data chunk. Only the RIFF wrapper differs.
func ContainerVariant(o WAVOptions) []byte {
	o.ExtraChunk = "LIST"
	if o.ExtraBytes == 0 {
		o.ExtraBytes = 26
	}
	return WAV(o)
}

func fmtPayload(rate, channels, bits, frameBytes int) []byte {
	p := make([]byte, 16)
	binary.LittleEndian.PutUint16(p[0:2], 1)
	binary.LittleEndian.PutUint16(p[2:4], uint16(channels))
	binary.LittleEndian.PutUint32(p[4:8], uint32(rate))
	binary.LittleEndian.PutUint32(p[8:12], uint32(rate*frameBytes))
	binary.LittleEndian.PutUint16(p[12:14], uint16(frameBytes))
	binary.LittleEndian.PutUint16(p[14:16], uint16(bits))
	return p
}

func chunk(id string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	copy(out[0:4], id)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
	copy(out[8:], payload)
	if len(payload)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func writeSample(dst []byte, v uint32, bits int) {
	switch bits {
	case 16:
		binary.LittleEndian.PutUint16(dst, uint16(v))
	case 24:
		dst[0] = byte(v)
		dst[1] = byte(v >> 8)
		dst[2] = byte(v >> 16)
	case 32:
		binary.LittleEndian.PutUint32(dst, v)
	}
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u32le(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// RTPOptions controls a variable-packet-length RTP stream.
type RTPOptions struct {
	SampleCounts []int64 // real samples carried by each packet
	Start        uint32  // first raw 32-bit timestamp
	DriftPPM     int64   // how far the timestamp clock runs ahead
	FrameBytes   int     // payload bytes per sample frame
	SSRC         uint32
}

// RTPPackets serializes RTP packets. Payload lengths differ per packet and the
// timestamps follow the real sample counts, with drift applied on top.
func RTPPackets(o RTPOptions) [][]byte {
	if o.FrameBytes <= 0 {
		o.FrameBytes = 2
	}
	if o.SSRC == 0 {
		o.SSRC = 0x4d564155
	}
	out := make([][]byte, 0, len(o.SampleCounts))
	var cumulative int64
	for i, samples := range o.SampleCounts {
		target := float64(cumulative) * (1 + float64(o.DriftPPM)/1_000_000)
		stamp := o.Start + uint32(math.Round(target))
		payload := make([]byte, int(samples)*o.FrameBytes)
		for j := range payload {
			payload[j] = byte((i*31 + j*17) & 0xff)
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i + 1),
				Timestamp:      stamp,
				SSRC:           o.SSRC,
			},
			Payload: payload,
		}
		raw, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		out = append(out, raw)
		cumulative += samples
	}
	return out
}

// VariablePattern returns 32 sample counts with six distinct packet lengths,
// so no average frame rate can reconstruct the timeline.
func VariablePattern() []int64 {
	pattern := []int64{3000, 7000, 4000, 6000, 5000, 8000}
	out := make([]int64, 32)
	for i := range out {
		out[i] = pattern[i%len(pattern)]
	}
	return out
}
