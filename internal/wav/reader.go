// Package wav parses PCM WAV containers and exposes block-streaming and
// exact-frame random access over the raw sample bytes. It never resamples,
// downmixes or converts sample formats.
package wav

import (
	"encoding/binary"
	"io"
	"os"

	"mediakernel/internal/clock"
	"mediakernel/internal/fsx"
)

// Header describes the validated PCM geometry of a WAV file.
type Header struct {
	SampleRate    uint32
	Channels      uint16
	BitsPerSample uint16
	// DataOffset is the absolute byte offset of the first sample frame.
	DataOffset int64
	// DataLength is the chunk-declared payload length in bytes.
	DataLength int64
	// FileLength is the actual size of the open file.
	FileLength int64
}

// FrameSize returns bytes per interleaved sample frame.
func (h Header) FrameSize() int64 {
	return int64(h.Channels) * int64(h.BitsPerSample) / 8
}

// Frames returns the number of complete frames declared by the data chunk.
func (h Header) Frames() int64 {
	return h.DataLength / h.FrameSize()
}

// Reader streams and randomly reads one PCM WAV file.
type Reader struct {
	file   *os.File
	header Header
}

const (
	riffHeaderLen = 12
	fmtPayloadLen = 16
	waveFormatPCM = 1
)

// Open resolves path inside base and parses the RIFF/fmt/data structure.
// Structural validation happens eagerly; payload truncation is reported at
// the first read attempt, carrying the failing byte offset.
func Open(base, path string) (*Reader, error) {
	f, clean, err := fsx.OpenInput(base, path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, clock.NewError("wav", clock.CatTruncated,
			"stat %q failed", clean).Wrap(err)
	}
	h, err := parseHeader(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Reader{file: f, header: h}, nil
}

// NewReader parses an already-open file (used by tests with temp files).
func NewReader(f *os.File) (*Reader, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, clock.NewError("wav", clock.CatTruncated, "stat failed").Wrap(err)
	}
	h, err := parseHeader(f, fi.Size())
	if err != nil {
		return nil, err
	}
	return &Reader{file: f, header: h}, nil
}

func parseHeader(r io.ReadSeeker, fileLen int64) (Header, error) {
	var h Header
	if fileLen < riffHeaderLen {
		return h, clock.NewError("wav", clock.CatTruncated,
			"file shorter than RIFF header").At(fileLen)
	}
	head := make([]byte, riffHeaderLen)
	if _, err := io.ReadFull(r, head); err != nil {
		return h, clock.NewError("wav", clock.CatTruncated, "RIFF header read").At(0).Wrap(err)
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return h, clock.NewError("wav", clock.CatUnsupported,
			"not a RIFF/WAVE container").At(0)
	}
	riffSize := int64(binary.LittleEndian.Uint32(head[4:8])) + 8
	if riffSize > fileLen {
		// RIFF claims bytes beyond the physical file: the difference is the
		// truncation region, reported at the first missing byte.
		return h, clock.NewError("wav", clock.CatTruncated,
			"RIFF size %d exceeds file length %d", riffSize, fileLen).At(fileLen)
	}

	var sawFmt, sawData bool
	var pos int64 = riffHeaderLen
	for pos+8 <= fileLen && !sawData {
		chunkHdr := make([]byte, 8)
		if _, err := io.ReadFull(r, chunkHdr); err != nil {
			return h, clock.NewError("wav", clock.CatTruncated,
				"chunk header truncated").At(pos).Wrap(err)
		}
		id := string(chunkHdr[0:4])
		size := int64(binary.LittleEndian.Uint32(chunkHdr[4:8]))
		payloadOff := pos + 8
		if size < 0 || payloadOff+size > fileLen {
			return h, clock.NewError("wav", clock.CatBadLength,
				"chunk %q length %d out of bounds (file=%d)", id, size, fileLen).At(pos)
		}
		switch id {
		case "fmt ":
			if sawFmt {
				return h, clock.NewError("wav", clock.CatBadLength,
					"duplicate fmt chunk").At(pos)
			}
			if size < fmtPayloadLen {
				return h, clock.NewError("wav", clock.CatBadLength,
					"fmt payload %d shorter than PCM minimum", size).At(pos)
			}
			payload := make([]byte, fmtPayloadLen)
			if _, err := io.ReadFull(r, payload); err != nil {
				return h, clock.NewError("wav", clock.CatTruncated,
					"fmt payload truncated").At(payloadOff).Wrap(err)
			}
			if tag := binary.LittleEndian.Uint16(payload[0:2]); tag != waveFormatPCM {
				return h, clock.NewError("wav", clock.CatUnsupported,
					"unsupported WAVE format tag %d (PCM only)", tag).At(payloadOff)
			}
			h.Channels = binary.LittleEndian.Uint16(payload[2:4])
			h.SampleRate = binary.LittleEndian.Uint32(payload[4:8])
			h.BitsPerSample = binary.LittleEndian.Uint16(payload[14:16])
			if h.SampleRate == 0 {
				return h, clock.NewError("wav", clock.CatInvalidParam,
					"zero sample rate").At(payloadOff + 4)
			}
			if h.Channels == 0 {
				return h, clock.NewError("wav", clock.CatInvalidParam,
					"zero channel count").At(payloadOff + 2)
			}
			if h.BitsPerSample == 0 || h.BitsPerSample%8 != 0 {
				return h, clock.NewError("wav", clock.CatBadLength,
					"invalid bits per sample %d", h.BitsPerSample).At(payloadOff + 14)
			}
			blockAlign := int64(binary.LittleEndian.Uint16(payload[12:14]))
			if blockAlign != h.FrameSize() {
				return h, clock.NewError("wav", clock.CatMisaligned,
					"block align %d != channels*bits/8 %d", blockAlign, h.FrameSize()).At(payloadOff + 12)
			}
			sawFmt = true
		case "data":
			if !sawFmt {
				return h, clock.NewError("wav", clock.CatBadLength,
					"data chunk precedes fmt chunk").At(pos)
			}
			if size%h.FrameSize() != 0 {
				return h, clock.NewError("wav", clock.CatMisaligned,
					"data length %d not aligned to frame size %d", size, h.FrameSize()).At(payloadOff)
			}
			h.DataOffset = payloadOff
			h.DataLength = size
			h.FileLength = fileLen
			sawData = true
		}
		pos = payloadOff + size + (size & 1) // chunks are word-padded
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return h, clock.NewError("wav", clock.CatTruncated, "chunk seek").At(pos).Wrap(err)
		}
	}
	if !sawFmt {
		return h, clock.NewError("wav", clock.CatNotFound, "no fmt chunk").At(0)
	}
	if !sawData {
		return h, clock.NewError("wav", clock.CatNotFound, "no data chunk").At(pos)
	}
	return h, nil
}

// Header returns the validated WAV geometry.
func (r *Reader) Header() Header { return r.header }

// Close releases the underlying file.
func (r *Reader) Close() error { return r.file.Close() }
