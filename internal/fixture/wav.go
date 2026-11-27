// Package fixture synthesizes deterministic multi-channel PCM WAV samples.
// All test and demo media is generated in-process; no external media files
// are referenced.
package fixture

import (
	"encoding/binary"
	"math"
	"os"

	"mediakernel/internal/clock"
	"mediakernel/internal/fsx"
)

// WAVOptions configures a generated PCM WAV file.
type WAVOptions struct {
	Channels      uint16
	SampleRate    uint32
	Frames        int
	BitsPerSample uint16
}

// FrameSize returns bytes per interleaved sample frame.
func (o WAVOptions) FrameSize() int {
	return int(o.Channels) * int(o.BitsPerSample) / 8
}

// SampleValue is the deterministic sample at (frame, channel). The same
// formula is used by generators and verifiers so reads can be checked.
func SampleValue(frame, channel int) int16 {
	return int16((frame*7919 + channel*104729) % 20000)
}

const (
	riffHeaderLen = 12
	fmtChunkLen   = 24 // 8-byte chunk header + 16 PCM payload
	dataHeaderLen = 8
)

// BuildWAV renders a complete little-endian PCM WAV byte stream.
func BuildWAV(o WAVOptions) ([]byte, error) {
	if o.Channels == 0 {
		return nil, clock.NewError("fixture", clock.CatInvalidParam,
			"channel count must be non-zero")
	}
	if o.SampleRate == 0 {
		return nil, clock.NewError("fixture", clock.CatInvalidParam,
			"sample rate must be non-zero")
	}
	if o.BitsPerSample == 0 || o.BitsPerSample%8 != 0 {
		return nil, clock.NewError("fixture", clock.CatInvalidParam,
			"bits per sample %d must be a positive multiple of 8", o.BitsPerSample)
	}
	if o.Frames < 0 {
		return nil, clock.NewError("fixture", clock.CatInvalidParam,
			"frame count must be non-negative")
	}
	frameSize := o.FrameSize()
	dataLen := o.Frames * frameSize
	if uint64(dataLen) > math.MaxUint32-riffHeaderLen-fmtChunkLen {
		return nil, clock.NewError("fixture", clock.CatOverflow,
			"generated WAV exceeds 32-bit RIFF size")
	}

	buf := make([]byte, riffHeaderLen+fmtChunkLen+dataHeaderLen+dataLen)
	le := binary.LittleEndian
	copy(buf[0:4], "RIFF")
	le.PutUint32(buf[4:8], uint32(4+(fmtChunkLen)+(dataHeaderLen)+dataLen))
	copy(buf[8:12], "WAVE")

	copy(buf[12:16], "fmt ")
	le.PutUint32(buf[16:20], 16)
	le.PutUint16(buf[20:22], 1) // WAVE_FORMAT_PCM
	le.PutUint16(buf[22:24], o.Channels)
	le.PutUint32(buf[24:28], o.SampleRate)
	le.PutUint32(buf[28:32], o.SampleRate*uint32(frameSize))
	le.PutUint16(buf[32:34], uint16(frameSize))
	le.PutUint16(buf[34:36], o.BitsPerSample)

	dataOff := riffHeaderLen + fmtChunkLen
	copy(buf[dataOff:dataOff+4], "data")
	le.PutUint32(buf[dataOff+4:dataOff+8], uint32(dataLen))
	payload := buf[dataOff+dataHeaderLen:]
	for f := 0; f < o.Frames; f++ {
		for c := 0; c < int(o.Channels); c++ {
			pos := (f*int(o.Channels) + c) * int(o.BitsPerSample) / 8
			le.PutUint16(payload[pos:pos+2], uint16(SampleValue(f, c)))
		}
	}
	return buf, nil
}

// WriteWAV builds the WAV and atomically writes it under the sandbox base.
func WriteWAV(base, relPath string, o WAVOptions) (string, error) {
	buf, err := BuildWAV(o)
	if err != nil {
		return "", err
	}
	clean, err := fsx.EnsureOutput(base, relPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(clean, buf, 0o644); err != nil {
		return "", clock.NewError("fixture", clock.CatInvalidParam,
			"write %q failed", clean).Wrap(err)
	}
	return clean, nil
}

// TruncateTail cuts tailBytes from the end, producing a data chunk whose
// declared length exceeds the bytes present on disk.
func TruncateTail(wav []byte, tailBytes int) ([]byte, error) {
	if tailBytes <= 0 || tailBytes >= len(wav) {
		return nil, clock.NewError("fixture", clock.CatInvalidParam,
			"truncation %d out of range for %d-byte WAV", tailBytes, len(wav))
	}
	out := make([]byte, len(wav)-tailBytes)
	copy(out, wav[:len(wav)-tailBytes])
	return out, nil
}

// MutateDataLength overwrites the data chunk's declared length without
// touching the payload, simulating an illegal/out-of-bounds chunk length.
func MutateDataLength(wav []byte, declared uint32) []byte {
	out := make([]byte, len(wav))
	copy(out, wav)
	dataOff := riffHeaderLen + fmtChunkLen
	binary.LittleEndian.PutUint32(out[dataOff+4:dataOff+8], declared)
	return out
}
