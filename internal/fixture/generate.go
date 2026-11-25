// Package fixture generates deterministic, self-contained Annex B
// elementary streams and matching keyframe indexes. No external media is
// referenced: every byte is derived from the frame number so repeated
// generation yields byte-identical fixtures and hashes.
package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"mediad/internal/stream"
)

// StartCode is the three-byte Annex B prefix used by every generated NAL.
var StartCode = []byte{0x00, 0x00, 0x01}

const (
	IndexExact   = "exact"
	IndexShifted = "shifted"
)

// Spec describes one fixture to generate.
type Spec struct {
	Codec        stream.Codec
	Frames       int
	IndexMode    string
	Truncate     int // 0 = whole stream; otherwise keep N full frames + a partial AU
	PayloadBytes int // deterministic NAL payload size per frame
}

// Fixture is the generated material plus the offsets needed for verification.
type Fixture struct {
	Stream        []byte
	IndexText     []byte
	FrameOffsets  []int64 // full-layout AU start offset per frame
	ShiftedFrames []uint64
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// shiftedSet is the fixed small set of frames whose index offset is nudged
// by one byte in shifted mode. Keeping it small holds the valid-entry hit
// rate inside the required 0.8–1.0 band.
var shiftedSet = map[uint64]bool{2: true, 9: true}

// Generate builds the stream and index described by spec.
func Generate(spec Spec) (*Fixture, error) {
	if spec.Frames <= 0 {
		return nil, fmt.Errorf("frames must be positive, got %d", spec.Frames)
	}
	if spec.PayloadBytes <= 0 {
		spec.PayloadBytes = 16
	}
	full, offsets, err := buildStream(spec)
	if err != nil {
		return nil, err
	}
	out := &Fixture{FrameOffsets: offsets}
	if spec.Truncate > 0 && spec.Truncate < spec.Frames {
		out.Stream = truncateStream(full, offsets, spec.Truncate, spec.Codec)
	} else {
		out.Stream = full
	}
	out.IndexText = buildIndex(spec, offsets)
	for f := range shiftedSet {
		out.ShiftedFrames = append(out.ShiftedFrames, f)
	}
	return out, nil
}

// buildStream emits one keyframe access unit per frame and records each AU
// start offset. Every frame is a keyframe, so an exact index reaches a 1.0
// hit rate while shifted entries are provable misses.
func buildStream(spec Spec) ([]byte, []int64, error) {
	var buf []byte
	offsets := make([]int64, spec.Frames)
	for f := 0; f < spec.Frames; f++ {
		offsets[f] = int64(len(buf))
		au, err := frameNALs(spec.Codec, uint64(f), spec.PayloadBytes)
		if err != nil {
			return nil, nil, err
		}
		buf = append(buf, au...)
	}
	return buf, offsets, nil
}

func frameNALs(codec stream.Codec, frame uint64, payload int) ([]byte, error) {
	pay := deterministicPayload(frame, payload)
	switch codec {
	case stream.H264:
		var b []byte
		b = append(b, nal(0x49)...)                 // AUD type 9, ref_idc=2
		b = append(b, append(nal(0x65), pay...)...) // IDR type 5
		return b, nil
	case stream.HEVC:
		var b []byte
		b = append(b, nal(35<<1, 0x01)...)                 // AUD
		b = append(b, append(nal(19<<1, 0x01), pay...)...) // IDR_W_RADL
		return b, nil
	case stream.AV1:
		var b []byte
		b = append(b, nal(2<<3)...)                 // temporal delimiter
		b = append(b, append(nal(1<<3), pay...)...) // sequence header = keyframe
		b = append(b, append(nal(6<<3), pay...)...) // frame OBU
		return b, nil
	default:
		return nil, fmt.Errorf("unsupported codec %q", codec)
	}
}

// nal prefixes header bytes with an Annex B start code.
func nal(header ...byte) []byte {
	out := append([]byte{}, StartCode...)
	return append(out, header...)
}

// deterministicPayload derives payload bytes from the frame number so each
// frame hashes differently while regeneration stays byte-identical.
func deterministicPayload(frame uint64, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte((frame*31 + uint64(i)*7 + 1) & 0xff)
	}
	return out
}

// truncateStream keeps the first keep full frames then appends a partial
// access unit whose NAL is below its minimum payload size, forcing the
// scanner to report a corrupt region at a known offset.
func truncateStream(full []byte, offsets []int64, keep int, codec stream.Codec) []byte {
	cut := offsets[keep]
	out := append([]byte(nil), full[:cut]...)
	out = append(out, StartCode...)
	switch codec {
	case stream.HEVC:
		out = append(out, 19<<1, 0x01) // 2-byte IDR header, payload < 3 -> truncated
	case stream.AV1:
		out = append(out, 0x80) // obu_forbidden_bit set -> unsupported header
	default: // H.264
		out = append(out, 0x65) // IDR header only, payload < 2 -> truncated
	}
	return out
}

// buildIndex renders the line-oriented keyframe index. In shifted mode the
// fixed shiftedSet entries point one byte past the real AU start.
func buildIndex(spec Spec, offsets []int64) []byte {
	typeLabel := map[stream.Codec]string{
		stream.H264: "IDR",
		stream.HEVC: "IDR_W_RADL",
		stream.AV1:  "KEY",
	}[spec.Codec]
	var b []byte
	for f, off := range offsets {
		o := off
		if spec.IndexMode == IndexShifted && shiftedSet[uint64(f)] {
			o++
		}
		b = append(b, fmt.Sprintf("%d %d %s\n", f, o, typeLabel)...)
	}
	return b
}
