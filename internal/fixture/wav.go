package fixture

import (
	"encoding/binary"
	"fmt"
)

// WAVSpec describes a generated PCM WAVE file.
type WAVSpec struct {
	Channels   int
	Bits       int
	SampleRate uint32
	Frames     int
	RF64       bool
	// Seed varies the deterministic sample ramp between layers.
	Seed int
}

// WAVData is a generated WAVE file plus the location of its sample bytes.
type WAVData struct {
	Bytes      []byte
	DataOffset int64
	DataSize   int64
	BlockAlign int
	Frames     int
}

// BuildWAV emits a RIFF (or RF64) PCM file with a deterministic sample ramp.
// Sample values are signed PCM of the requested depth, so a remux must copy
// them byte-for-byte to preserve content.
func BuildWAV(spec WAVSpec) (WAVData, error) {
	if spec.Channels <= 0 || spec.Frames < 0 {
		return WAVData{}, fmt.Errorf("invalid wav spec")
	}
	switch spec.Bits {
	case 16, 24, 32:
	default:
		return WAVData{}, fmt.Errorf("unsupported bit depth %d", spec.Bits)
	}
	if spec.SampleRate == 0 {
		return WAVData{}, fmt.Errorf("invalid sample rate")
	}
	block := spec.Channels * spec.Bits / 8
	samples := spec.Frames * spec.Channels
	data := make([]byte, samples*block/spec.Channels)
	for i := 0; i < spec.Frames; i++ {
		for c := 0; c < spec.Channels; c++ {
			// Distinct, bounded ramp per channel; low bits exercise the
			// byte-exactness check for 24-bit storage.
			v := int32((i*37 + c*911 + spec.Seed*13) % 60000)
			v -= 30000
			off := i*block + c*(spec.Bits/8)
			switch spec.Bits {
			case 16:
				binary.LittleEndian.PutUint16(data[off:], uint16(int16(v)))
			case 24:
				u := uint32(v) & 0xFFFFFF
				data[off] = byte(u)
				data[off+1] = byte(u >> 8)
				data[off+2] = byte(u >> 16)
			case 32:
				binary.LittleEndian.PutUint32(data[off:], uint32(v))
			}
		}
	}

	var body []byte
	appendChunk := func(id string, payload []byte) {
		h := make([]byte, 8)
		copy(h, id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(payload)))
		body = append(body, h...)
		body = append(body, payload...)
		if len(payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	fmtChunk := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtChunk[0:], 1)
	binary.LittleEndian.PutUint16(fmtChunk[2:], uint16(spec.Channels))
	binary.LittleEndian.PutUint32(fmtChunk[4:], spec.SampleRate)
	binary.LittleEndian.PutUint32(fmtChunk[8:], spec.SampleRate*uint32(block))
	binary.LittleEndian.PutUint16(fmtChunk[12:], uint16(block))
	binary.LittleEndian.PutUint16(fmtChunk[14:], uint16(spec.Bits))
	appendChunk("fmt ", fmtChunk)

	header := make([]byte, 12)
	copy(header, "RIFF")
	copy(header[8:], "WAVE")
	preData := len(header) + len(body) + 8 // 12-byte RIFF header + chunks + data header
	dataOffset := int64(preData)
	out := append([]byte(nil), header...)
	out = append(out, body...)
	dh := make([]byte, 8)
	copy(dh, "data")
	binary.LittleEndian.PutUint32(dh[4:], uint32(len(data)))
	out = append(out, dh...)
	out = append(out, data...)
	if len(data)%2 == 1 {
		out = append(out, 0)
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return WAVData{Bytes: out, DataOffset: dataOffset, DataSize: int64(len(data)),
		BlockAlign: block, Frames: spec.Frames}, nil
}

// SplitFrames slices n complete frames starting at frame index.
func (w WAVData) SplitFrames(startFrame, n int) ([]byte, bool) {
	start := int64(startFrame * w.BlockAlign)
	length := int64(n * w.BlockAlign)
	if start+length > int64(len(w.Bytes))-w.DataOffset-0 && start+length > w.DataSize {
		return nil, false
	}
	return w.Bytes[w.DataOffset+start : w.DataOffset+start+length], true
}
