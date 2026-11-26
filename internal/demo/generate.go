// Package demo wires the reusable media libraries into the deterministic
// acceptance scenario used by the medcheck command.
package demo

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/zld/medcheck/pkg/bitstream"
)

// KeyframesPerCodec is the fixed acceptance count for each generated codec.
const KeyframesPerCodec = 12

// StreamFixture is generated elementary-stream source material.
type StreamFixture struct {
	Codec      bitstream.Codec
	TrackID    string
	Data       []byte
	KeyOffsets []int64
	PTS90k     []int64
}

// GenerateStream deterministically generates one codec stream with 12 clean
// keyframes. Payload bytes avoid zero runs that could imitate start codes.
func GenerateStream(codec bitstream.Codec, seed int64) (*StreamFixture, error) {
	f := &StreamFixture{Codec: codec, TrackID: "cam-" + string(codec)}
	r := rand.New(rand.NewSource(seed + int64(codecSeed(codec))))
	for i := 0; i < KeyframesPerCodec; i++ {
		f.PTS90k = append(f.PTS90k, int64(i)*3000)
		payload := deterministicPayload(r, 32+i)
		switch codec {
		case bitstream.CodecH264:
			f.Data = append(f.Data, 0, 0, 0, 1, 0x67, 0x42, 0x00, 0x1f)
			f.Data = append(f.Data, 0, 0, 0, 1, 0x68, 0xce, 0x38, 0x80)
			f.KeyOffsets = append(f.KeyOffsets, int64(len(f.Data)))
			f.Data = append(f.Data, annexB(0x65, payload)...)
			f.Data = append(f.Data, annexB(0x41, deterministicPayload(r, 12+i))...)
		case bitstream.CodecH265:
			f.Data = append(f.Data, 0, 0, 0, 1, (33<<1)&0x7e, 0x01, 0x11, 0x22)
			f.Data = append(f.Data, 0, 0, 0, 1, (34<<1)&0x7e, 0x01, 0x33, 0x44)
			f.KeyOffsets = append(f.KeyOffsets, int64(len(f.Data)))
			f.Data = append(f.Data, annexBH265(19, payload)...)
			f.Data = append(f.Data, annexBH265(1, deterministicPayload(r, 10+i))...)
		case bitstream.CodecAV1:
			f.Data = appendOBU(f.Data, 1, []byte{0x01, 0x0c, byte(4 + i)})
			f.KeyOffsets = append(f.KeyOffsets, int64(len(f.Data)))
			keyPayload := append([]byte{0x00, 0x10, byte(i)}, payload...)
			f.Data = appendOBU(f.Data, 6, keyPayload)
			f.Data = appendOBU(f.Data, 6, append([]byte{0x20, 0x20}, deterministicPayload(r, 10+i)...))
		default:
			return nil, fmt.Errorf("unsupported codec %q", codec)
		}
	}
	return f, nil
}

func codecSeed(c bitstream.Codec) int {
	switch c {
	case bitstream.CodecH264:
		return 100
	case bitstream.CodecH265:
		return 200
	default:
		return 300
	}
}

func deterministicPayload(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(250) + 2) // never 0x00 or 0x01-heavy enough for start codes
	}
	return b
}

func annexB(header byte, payload []byte) []byte {
	out := []byte{0, 0, 0, 1, header}
	return append(out, payload...)
}

func annexBH265(nalType byte, payload []byte) []byte {
	out := []byte{0, 0, 0, 1, (nalType << 1) & 0x7e, 0x01}
	return append(out, payload...)
}

func appendOBU(dst []byte, typ byte, payload []byte) []byte {
	if len(payload) >= 128 {
		var size [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(size[:], uint64(len(payload)))
		dst = append(dst, (typ<<3)|0x02)
		dst = append(dst, size[:n]...)
		return append(dst, payload...)
	}
	dst = append(dst, (typ<<3)|0x02, byte(len(payload)))
	return append(dst, payload...)
}

// CorruptFixtures returns inputs that must produce explicit error records.
func CorruptFixtures() map[string][]byte {
	h264Trunc := []byte{0, 0, 0, 1, 0x65, 0x80, 0x12, 0, 0, 0, 1, 0x65}
	// A resync keyframe after the header-only NAL proves parsing can continue.
	h264Trunc = append(h264Trunc, annexB(0x65, []byte{0xaa, 0xbb, 0xcc, 0xdd})...)
	av1Illegal := []byte{(5 << 3) | 0x02}
	for i := 0; i < 10; i++ {
		av1Illegal = append(av1Illegal, 0x80)
	}
	av1Trunc := []byte{(5 << 3) | 0x02, 12, 0xaa, 0xbb}
	return map[string][]byte{
		"corrupt/h264-truncated.h264":    h264Trunc,
		"corrupt/av1-illegal-length.obu": av1Illegal,
		"corrupt/av1-truncated.obu":      av1Trunc,
	}
}
