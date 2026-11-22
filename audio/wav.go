// Package audio implements the streaming DSP pipeline: WAV decoding,
// stateful resampling, channel layout normalization and loudness control.
package audio

import (
	"bytes"
	"encoding/binary"
	"io"

	"medpipe/timeline"
)

// SupportedPCMBits is the exhaustive set of accepted sample widths.
var SupportedPCMBits = map[uint16]bool{16: true, 24: true, 32: true}

// WAVHeader describes the decoded format of a WAV container.
type WAVHeader struct {
	SampleRate   uint32
	Channels     uint16
	BitsPerSample uint16
	DataBytes    uint32
}

// Frames de-interleaves normalized samples into per-channel frame slices.
func Frames(interleaved []float64, channels int) [][]float64 {
	if channels <= 0 {
		return nil
	}
	out := make([][]float64, channels)
	frames := len(interleaved) / channels
	for c := range out {
		out[c] = make([]float64, frames)
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			out[c][i] = interleaved[i*channels+c]
		}
	}
	return out
}

// Interleave is the inverse of Frames.
func Interleave(frames [][]float64) []float64 {
	if len(frames) == 0 {
		return nil
	}
	ch := len(frames)
	n := len(frames[0])
	out := make([]float64, n*ch)
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			out[i*ch+c] = frames[c][i]
		}
	}
	return out
}

// DecodePCM converts raw little-endian PCM bytes to normalized float64
// samples in [-1, 1). Widths outside 16/24/32 and payloads that do not tile
// the channel grid are rejected, never padded silently.
func DecodePCM(data []byte, bitsPerSample uint16, channels uint16) ([]float64, error) {
	if !SupportedPCMBits[bitsPerSample] {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
			"unsupported PCM width %d bits (only 16/24/32)", bitsPerSample)
	}
	if channels == 0 {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
			"channel count must be non-zero")
	}
	bytesPerSample := int(bitsPerSample) / 8
	frameBytes := bytesPerSample * int(channels)
	if len(data)%frameBytes != 0 {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
			"payload %d bytes is not a multiple of frame size %d", len(data), frameBytes)
	}
	out := make([]float64, len(data)/bytesPerSample)
	switch bitsPerSample {
	case 16:
		for i := range out {
			v := int16(binary.LittleEndian.Uint16(data[i*2:]))
			out[i] = float64(v) / 32768.0
		}
	case 24:
		for i := range out {
			off := i * 3
			v := uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16
			if v&0x800000 != 0 { // sign-extend 24-bit two's complement
				v |= 0xFF000000
			}
			out[i] = float64(int32(v)) / 8388608.0
		}
	case 32:
		for i := range out {
			v := int32(binary.LittleEndian.Uint32(data[i*4:]))
			out[i] = float64(v) / 2147483648.0
		}
	}
	return out, nil
}

// EncodePCM is the inverse of DecodePCM, used by the fixture generator.
func EncodePCM(samples []float64, bitsPerSample uint16) ([]byte, error) {
	if !SupportedPCMBits[bitsPerSample] {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
			"unsupported PCM width %d bits", bitsPerSample)
	}
	bps := int(bitsPerSample) / 8
	out := make([]byte, len(samples)*bps)
	clamp := func(x float64) float64 {
		if x > 1 {
			return 1
		}
		if x < -1 {
			return -1
		}
		return x
	}
	for i, s := range samples {
		s = clamp(s)
		switch bitsPerSample {
		case 16:
			binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(s*32767.0)))
		case 24:
			v := uint32(int32(s*8388607.0)) & 0x00FFFFFF
			out[i*3] = byte(v)
			out[i*3+1] = byte(v >> 8)
			out[i*3+2] = byte(v >> 16)
		case 32:
			binary.LittleEndian.PutUint32(out[i*4:], uint32(int32(s*2147483647.0)))
		}
	}
	return out, nil
}

// ParseWAV reads a complete RIFF/WAVE container. Truncated payloads, bad
// magic values and non-PCM formats produce located timeline errors.
func ParseWAV(r io.Reader) (*WAVHeader, []float64, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "", "read failed: %v", err)
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, nil, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
			"missing RIFF/WAVE magic (got %d bytes)", len(raw))
	}
	if binary.LittleEndian.Uint32(raw[4:8])+8 != uint32(len(raw)) {
		return nil, nil, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
			"RIFF size field %d does not match container length %d",
			binary.LittleEndian.Uint32(raw[4:8])+8, len(raw))
	}
	h := &WAVHeader{}
	var pcm []byte
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if body+size > len(raw) {
			return nil, nil, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
				"chunk %q declares %d bytes but container ends at %d", id, size, len(raw)-body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
					"fmt chunk too short: %d bytes", size)
			}
			if tag := binary.LittleEndian.Uint16(raw[body:]); tag != 1 {
				return nil, nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
					"only PCM (format tag 1) supported, got %d", tag)
			}
			h.Channels = binary.LittleEndian.Uint16(raw[body+2:])
			h.SampleRate = binary.LittleEndian.Uint32(raw[body+4:])
			h.BitsPerSample = binary.LittleEndian.Uint16(raw[body+14:])
			if !SupportedPCMBits[h.BitsPerSample] {
				return nil, nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageWAV, "",
					"unsupported PCM width %d bits", h.BitsPerSample)
			}
		case "data":
			pcm = append([]byte(nil), raw[body:body+size]...)
			h.DataBytes = uint32(size)
		}
		pos = body + size + size%2 // chunks are word-aligned
	}
	if h.SampleRate == 0 || h.Channels == 0 {
		return nil, nil, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
			"fmt chunk missing")
	}
	if pcm == nil {
		return nil, nil, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
			"data chunk missing")
	}
	samples, err := DecodePCM(pcm, h.BitsPerSample, h.Channels)
	if err != nil {
		return nil, nil, err
	}
	return h, samples, nil
}

// WAVPayloadOffset returns the byte offset of the data chunk payload in a
// RIFF/WAVE container, or a located error when the container is malformed.
func WAVPayloadOffset(raw []byte) (int, error) {
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return 0, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
			"missing RIFF/WAVE magic")
	}
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if body+size > len(raw) {
			return 0, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "",
				"chunk %q truncated", id)
		}
		if id == "data" {
			return body, nil
		}
		pos = body + size + size%2
	}
	return 0, timeline.NewError(timeline.ErrCodeTruncatedWAV, timeline.StageWAV, "", "data chunk missing")
}

// WriteWAV serializes normalized samples into a minimal PCM RIFF/WAVE file.
func WriteWAV(w io.Writer, h WAVHeader, samples []float64) error {
	pcm, err := EncodePCM(samples, h.BitsPerSample)
	if err != nil {
		return err
	}
	buf := bytes.NewBuffer(nil)
	putU32 := func(v uint32) { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); buf.Write(b[:]) }
	putU16 := func(v uint16) { var b [2]byte; binary.LittleEndian.PutUint16(b[:], v); buf.Write(b[:]) }
	buf.WriteString("RIFF")
	putU32(uint32(36 + len(pcm)))
	buf.WriteString("WAVEfmt ")
	putU32(16)
	putU16(1) // PCM
	putU16(h.Channels)
	putU32(h.SampleRate)
	putU32(uint32(h.SampleRate) * uint32(h.Channels) * uint32(h.BitsPerSample) / 8)
	putU16(h.Channels * h.BitsPerSample / 8)
	putU16(h.BitsPerSample)
	buf.WriteString("data")
	putU32(uint32(len(pcm)))
	buf.Write(pcm)
	_, err = w.Write(buf.Bytes())
	return err
}
