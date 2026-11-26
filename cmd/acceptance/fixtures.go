package main

import (
	"bytes"
	"encoding/binary"
	"math"

	"github.com/pion/rtp"
)

type toneSpec struct {
	SampleRate  int
	Channels    int
	Bits        int
	Frames      int
	Frequency   float64
	Amplitude16 int16
}

type pcmFixture struct {
	WAV     []byte
	PCM     []byte
	Samples []int32 // canonical left-aligned interleaved samples
	Rate    int
	Ch      int
	Bits    int
	Frames  int
}

func buildWAV(rate, channels, bits int, pcm []byte) []byte {
	blockAlign := channels * bits / 8
	var b bytes.Buffer
	b.WriteString("RIFF")
	riffSize := uint32(36 + len(pcm) + (len(pcm) & 1))
	_ = binary.Write(&b, binary.LittleEndian, riffSize)
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	if len(pcm)&1 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func genTone(s toneSpec) *pcmFixture {
	if s.Bits != 16 {
		s.Bits = 16 // acceptance source evidence uses 16-bit PCM
	}
	if s.Amplitude16 == 0 {
		s.Amplitude16 = 18000
	}
	frameBytes := s.Channels * 2
	pcm := make([]byte, s.Frames*frameBytes)
	samples := make([]int32, s.Frames*s.Channels)
	for f := 0; f < s.Frames; f++ {
		for c := 0; c < s.Channels; c++ {
			phase := 2 * math.Pi * s.Frequency * float64(f) / float64(s.SampleRate)
			// Small channel-dependent phase keeps mapping/stereo checks honest.
			v := int16(float64(s.Amplitude16) * math.Sin(phase+0.01*float64(c)))
			off := f*frameBytes + c*2
			binary.LittleEndian.PutUint16(pcm[off:off+2], uint16(v))
			samples[f*s.Channels+c] = int32(v) << 16
		}
	}
	return &pcmFixture{
		WAV: buildWAV(s.SampleRate, s.Channels, s.Bits, pcm), PCM: pcm, Samples: samples,
		Rate: s.SampleRate, Ch: s.Channels, Bits: s.Bits, Frames: s.Frames,
	}
}

func badRIFFLengthWAV(good []byte) []byte {
	b := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(b[4:8], 0xffff_ff00)
	return b
}

func partialFrameWAV() []byte {
	// Mono 16-bit: one whole frame plus one orphan byte.
	return buildWAV(48000, 1, 16, []byte{0x10, 0x20, 0x30})
}

// packetize returns uniform PCM RTP packets with timestamp counted in frames.
func packetize(f *pcmFixture, startSeq uint16, packetFrames int) []*rtp.Packet {
	frameBytes := f.Ch * (f.Bits / 8)
	payloadLen := packetFrames * frameBytes
	var out []*rtp.Packet
	for off, seq := 0, startSeq; off < len(f.PCM); off, seq = off+payloadLen, seq+1 {
		end := off + payloadLen
		if end > len(f.PCM) {
			end = len(f.PCM)
		}
		frameOffset := off / frameBytes
		out = append(out, &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      uint32(frameOffset),
				SSRC:           0x4d43484b,
			},
			Payload: append([]byte(nil), f.PCM[off:end]...),
		})
	}
	return out
}

func marshalPackets(ps []*rtp.Packet) [][]byte {
	out := make([][]byte, 0, len(ps))
	for _, p := range ps {
		b, err := p.Marshal()
		if err != nil {
			panic(err) // fixtures are constructed valid
		}
		out = append(out, b)
	}
	return out
}

// shuffledWithDuplicates performs a deterministic local permutation and repeats
// a few packets. The radius stays inside the reorder window.
func shuffledWithDuplicates(ps []*rtp.Packet) []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(ps)+4)
	for i := 0; i < len(ps); i += 8 {
		end := i + 8
		if end > len(ps) {
			end = len(ps)
		}
		block := append([]*rtp.Packet(nil), ps[i:end]...)
		// Deterministic reversal of alternating adjacent pairs.
		for j := 0; j+1 < len(block); j += 2 {
			block[j], block[j+1] = block[j+1], block[j]
		}
		out = append(out, block...)
		if i == 16 || i == 80 {
			out = append(out, ps[i+1]) // duplicate an already sent packet
		}
	}
	return out
}

func removeSequences(ps []*rtp.Packet, remove ...uint16) []*rtp.Packet {
	bad := map[uint16]bool{}
	for _, s := range remove {
		bad[s] = true
	}
	var out []*rtp.Packet
	for _, p := range ps {
		if !bad[p.SequenceNumber] {
			out = append(out, p)
		}
	}
	return out
}
