package samplegen

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"

	"media-archiver/internal/media"
)

// TimestampChunk is the optional tms chunk written before each data chunk.
type TimestampChunk struct {
	Present bool
	NS      int64
}

// BlockSpec is one generated PCM block and its source timestamp.
type BlockSpec struct {
	Timestamp *TimestampChunk
	PCM       []byte
}

// WAVSpec describes a small multi-block integer-PCM RIFF file.
type WAVSpec struct {
	Format media.PCMFormat
	Blocks []BlockSpec
}

// EncodeWAV encodes the spec into deterministic RIFF/WAVE bytes.
func EncodeWAV(spec WAVSpec) ([]byte, error) {
	if err := spec.Format.Validate(); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	writeFormatChunk(&body, spec.Format)
	for _, b := range spec.Blocks {
		writeTimestampChunk(&body, b.Timestamp)
		if err := writeDataChunk(&body, b.PCM); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+body.Len()))
	out.WriteString("WAVE")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

func WriteWAV(path string, spec WAVSpec) error {
	data, err := EncodeWAV(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return media.WriteFileAtomic(path, data, 0o644)
}

func writeFormatChunk(buf *bytes.Buffer, f media.PCMFormat) {
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, f.AudioFormat)
	_ = binary.Write(buf, binary.LittleEndian, f.Channels)
	_ = binary.Write(buf, binary.LittleEndian, f.SampleRate)
	_ = binary.Write(buf, binary.LittleEndian, f.ByteRate)
	_ = binary.Write(buf, binary.LittleEndian, f.BlockAlign)
	_ = binary.Write(buf, binary.LittleEndian, f.BitsPerSample)
}

func writeTimestampChunk(buf *bytes.Buffer, ts *TimestampChunk) {
	buf.WriteString("tms ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	var flags uint32
	var ns int64
	if ts != nil && ts.Present {
		flags = 1
		ns = ts.NS
	}
	_ = binary.Write(buf, binary.LittleEndian, flags)
	_ = binary.Write(buf, binary.LittleEndian, uint32(0))
	_ = binary.Write(buf, binary.LittleEndian, uint64(ns))
}

func writeDataChunk(buf *bytes.Buffer, pcm []byte) error {
	buf.WriteString("data")
	if uint64(len(pcm)) > uint64(^uint32(0)) {
		return media.NewError(media.CodeBlockTooLarge, media.StageParse, -1, "PCM chunk too large", nil)
	}
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	if len(pcm)%2 == 1 {
		buf.WriteByte(0)
	}
	return nil
}
