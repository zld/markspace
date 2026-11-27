// Package wav parses PCM WAV containers and their multi-channel sample
// blocks. Parsing never mutates the input: truncated blocks are reported
// with their raw byte offsets while preceding complete blocks stay valid.
package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"rtx/internal/model"
)

// Header is the validated PCM WAV metadata.
type Header struct {
	Channels       uint16
	SampleRate     uint32
	BitsPerSample  uint16
	BlockAlign     uint16
	ByteRate       uint32
	DataOffset     int64 // raw byte offset of the first data byte
	DataLength     uint32 // declared data chunk size
	DeclaredTrunc  bool  // declared data runs past the physical file end
	FileSize       int64
}

// FrameCount returns the number of complete interleaved frames the data
// chunk can hold according to its declared size.
func (h Header) FrameCount() uint64 {
	if h.BlockAlign == 0 {
		return 0
	}
	return uint64(h.DataLength) / uint64(h.BlockAlign)
}

var (
	riffTag = []byte("RIFF")
	waveTag = []byte("WAVE")
	fmtTag  = []byte("fmt ")
	dataTag = []byte("data")
)

// ParseHeader validates the RIFF/WAVE structure and locates the data
// chunk. Unknown chunks are skipped. A malformed header is a fatal
// structured error (exit code 1 at the CLI boundary).
func ParseHeader(buf []byte) (*Header, error) {
	if len(buf) < 12 {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			0, int64(len(buf)), "file shorter than RIFF preamble",
			"reject input", errors.New("short file"))
	}
	if !bytes.Equal(buf[0:4], riffTag) || !bytes.Equal(buf[8:12], waveTag) {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			0, 0, "missing RIFF/WAVE signature", "reject input", nil)
	}
	pos := 12
	var hdr *Header
	for pos+8 <= len(buf) {
		id := buf[pos : pos+4]
		size := int(binary.LittleEndian.Uint32(buf[pos+4 : pos+8]))
		payload := pos + 8
		declaredEnd := payload + size
		if declaredEnd > len(buf) {
			if !bytes.Equal(id, dataTag) {
				return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
					int64(pos), int64(size),
					fmt.Sprintf("chunk %q payload exceeds file", string(id)),
					"reject input", errors.New("truncated chunk"))
			}
			// A truncated data region is recoverable: ParseBlocks reports
			// the partial block offset and keeps earlier complete blocks.
		}
		if bytes.Equal(id, fmtTag) {
			h, err := parseFmt(buf[payload:min(declaredEnd, len(buf))])
			if err != nil {
				return nil, err
			}
			hdr = h
		} else if bytes.Equal(id, dataTag) {
			if hdr == nil {
				return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
					int64(pos), 0, "data chunk precedes fmt chunk",
					"reject input", nil)
			}
			hdr.DataOffset = int64(payload)
			hdr.DataLength = uint32(size)
			hdr.DeclaredTrunc = declaredEnd > len(buf)
			hdr.FileSize = int64(len(buf))
			return hdr, nil
		}
		if declaredEnd > len(buf) {
			break // already handled: only the data chunk may overrun
		}
		pos = declaredEnd + size&1 // chunks are word aligned
	}
	return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
		int64(pos), 0, "no data chunk found", "reject input", nil)
}

func parseFmt(p []byte) (*Header, error) {
	if len(p) < 16 {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			0, int64(len(p)), "fmt chunk shorter than 16 bytes",
			"reject input", errors.New("short fmt"))
	}
	format := binary.LittleEndian.Uint16(p[0:2])
	channels := binary.LittleEndian.Uint16(p[2:4])
	rate := binary.LittleEndian.Uint32(p[4:8])
	byteRate := binary.LittleEndian.Uint32(p[8:12])
	blockAlign := binary.LittleEndian.Uint16(p[12:14])
	bits := binary.LittleEndian.Uint16(p[14:16])
	if format != 1 { // PCM only
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			0, int64(format), fmt.Sprintf("unsupported audio format %d (PCM only)", format),
			"reject input", nil)
	}
	if channels == 0 || rate == 0 {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			0, 0, "zero channels or sample rate", "reject input", nil)
	}
	if bits != 16 && bits != 24 && bits != 32 {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			0, int64(bits), "only 16/24/32-bit PCM is supported",
			"reject input", nil)
	}
	if want := channels * (bits / 8); blockAlign != want {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			12, int64(blockAlign),
			fmt.Sprintf("block align %d != channels*bytes %d", blockAlign, want),
			"reject input", nil)
	}
	if want := rate * uint32(blockAlign); byteRate != want {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonHeader,
			8, int64(byteRate),
			fmt.Sprintf("byte rate %d != rate*blockAlign %d", byteRate, want),
			"reject input", nil)
	}
	return &Header{
		Channels:      channels,
		SampleRate:    rate,
		BitsPerSample: bits,
		BlockAlign:    blockAlign,
		ByteRate:      byteRate,
	}, nil
}
