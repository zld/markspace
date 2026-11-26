// Package wav performs strict RIFF/WAVE validation and streaming PCM frame
// decoding. Metadata and one small frame are the only normally buffered data.
package wav

import (
	"encoding/binary"
	"errors"
	"io"
	"math"

	"mediacheck/internal/timebase"
)

const (
	FormatPCM     uint16 = 1
	MaxChannels          = 8
	MinSampleRate        = 8000
	MaxSampleRate        = 192000
)

// Metadata is the validated container/fmt/data description.
type Metadata struct {
	FileSize       int64  `json:"file_size"`
	RIFFSize       uint32 `json:"riff_size"`
	AudioFormat    uint16 `json:"audio_format"`
	Channels       int    `json:"channels"`
	SampleRate     int    `json:"sample_rate"`
	BitsPerSample  int    `json:"bits_per_sample"`
	ByteRate       uint32 `json:"byte_rate"`
	BlockAlign     int    `json:"block_align"`
	DataOffset     int64  `json:"data_offset"`
	DataSize       int64  `json:"data_size"`
	FrameCount     int64  `json:"frame_count"`
	FirstBadOffset int64  `json:"first_bad_offset"`
}

// Reader streams complete, frame-aligned PCM bytes from an io.ReaderAt.
type Reader struct {
	at        io.ReaderAt
	meta      Metadata
	data      *io.SectionReader
	framePos  int64
	frameSize []byte
}

// Meta returns a copy of the validated metadata.
func (r *Reader) Meta() Metadata { return r.meta }

// FrameIndex returns the next frame index.
func (r *Reader) FrameIndex() int64 { return r.framePos }

func readAt(r io.ReaderAt, off int64, b []byte, code timebase.Code, what string) (int64, error) {
	n, err := io.ReadFull(io.NewSectionReader(r, off, int64(len(b))), b)
	if err == nil {
		return off + int64(n), nil
	}
	bad := off + int64(n)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return bad, timebase.At(code, bad, "truncated %s at byte %d: %v", what, bad, err)
	}
	return bad, timebase.At(code, bad, "read %s: %v", what, err)
}

func u16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func u32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func checkField(cond bool, code timebase.Code, offset int64, format string, args ...any) error {
	if cond {
		return nil
	}
	return timebase.At(code, offset, format, args...)
}

func checkedAdd(a, b int64) (int64, bool) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

// OpenAt validates a RIFF/WAVE PCM source of known fileSize and returns a
// frame reader positioned at the first audio frame.
func OpenAt(r io.ReaderAt, fileSize int64) (*Reader, error) {
	if r == nil || fileSize < 12 {
		return nil, timebase.At(timebase.CodeRIFF, 0, "input is shorter than a RIFF/WAVE header")
	}
	meta := Metadata{FileSize: fileSize, FirstBadOffset: -1}
	off := int64(0)

	header := make([]byte, 12)
	next, err := readAt(r, off, header, timebase.CodeRIFF, "RIFF header")
	if err != nil {
		return nil, err
	}
	off = next
	if string(header[0:4]) != "RIFF" {
		return nil, timebase.At(timebase.CodeRIFF, 0, "missing RIFF magic")
	}
	meta.RIFFSize = u32(header[4:8])
	if string(header[8:12]) != "WAVE" {
		return nil, timebase.At(timebase.CodeRIFF, 8, "missing WAVE magic")
	}
	if uint64(meta.RIFFSize)+8 != uint64(fileSize) {
		return nil, timebase.At(timebase.CodeRIFF, 4,
			"RIFF size %d does not match file payload length %d", meta.RIFFSize, fileSize-8)
	}

	fmtSeen := false
	for off+8 <= fileSize {
		chunkStart := off
		ch := make([]byte, 8)
		off, err = readAt(r, off, ch, timebase.CodeRIFF, "chunk header")
		if err != nil {
			return nil, err
		}
		id := string(ch[0:4])
		size := int64(u32(ch[4:8]))
		body := off

		end, ok := checkedAdd(body, size)
		if !ok {
			return nil, timebase.At(timebase.CodeRIFF, chunkStart+4, "chunk %q size overflow", id)
		}
		if end > fileSize {
			return nil, timebase.At(timebase.CodeRIFF, chunkStart+4,
				"chunk %q length %d ends at %d beyond file %d", id, size, end, fileSize)
		}

		switch id {
		case "fmt ":
			if size < 16 {
				return nil, timebase.At(timebase.CodeFmt, body, "fmt chunk too short: %d", size)
			}
			fb := make([]byte, 16)
			_, err = readAt(r, body, fb, timebase.CodeFmt, "fmt body")
			if err != nil {
				return nil, err
			}
			meta.AudioFormat = u16(fb[0:2])
			meta.Channels = int(u16(fb[2:4]))
			meta.SampleRate = int(u32(fb[4:8]))
			meta.ByteRate = u32(fb[8:12])
			meta.BlockAlign = int(u16(fb[12:14]))
			meta.BitsPerSample = int(u16(fb[14:16]))

			if err := checkField(meta.AudioFormat == FormatPCM, timebase.CodeFmt, body,
				"unsupported audio format %d (only PCM=1)", meta.AudioFormat); err != nil {
				return nil, err
			}
			if err := checkField(meta.Channels >= 1 && meta.Channels <= MaxChannels,
				timebase.CodeFmt, body+2, "channels %d outside 1..8", meta.Channels); err != nil {
				return nil, err
			}
			if err := checkField(meta.SampleRate >= MinSampleRate && meta.SampleRate <= MaxSampleRate,
				timebase.CodeFmt, body+4, "sample rate %d outside 8000..192000", meta.SampleRate); err != nil {
				return nil, err
			}
			if err := checkField(meta.BitsPerSample == 16 || meta.BitsPerSample == 24 || meta.BitsPerSample == 32,
				timebase.CodeFmt, body+14, "bits %d not one of 16/24/32", meta.BitsPerSample); err != nil {
				return nil, err
			}
			wantAlign := meta.Channels * (meta.BitsPerSample / 8)
			if err := checkField(meta.BlockAlign == wantAlign, timebase.CodeFmt, body+12,
				"block align %d, want %d", meta.BlockAlign, wantAlign); err != nil {
				return nil, err
			}
			wantByteRate := meta.SampleRate * wantAlign
			if err := checkField(int64(meta.ByteRate) == int64(wantByteRate), timebase.CodeFmt, body+8,
				"byte rate %d, want %d", meta.ByteRate, wantByteRate); err != nil {
				return nil, err
			}
			fmtSeen = true
			off = end + (size & 1)

		case "data":
			if !fmtSeen {
				return nil, timebase.At(timebase.CodeFmt, chunkStart, "data precedes fmt chunk")
			}
			meta.DataOffset = body
			meta.DataSize = size
			if size%int64(meta.BlockAlign) != 0 {
				whole := size / int64(meta.BlockAlign)
				bad := body + whole*int64(meta.BlockAlign)
				meta.FirstBadOffset = bad
				return nil, timebase.At(timebase.CodeAlignment, bad,
					"data size %d leaves partial frame after %d complete frames (block_align=%d)",
					size, whole, meta.BlockAlign)
			}
			meta.FrameCount = size / int64(meta.BlockAlign)
			return &Reader{
				at:        r,
				meta:      meta,
				data:      io.NewSectionReader(r, body, size),
				frameSize: make([]byte, meta.BlockAlign),
			}, nil

		default:
			off = end + (size & 1)
		}
	}
	return nil, timebase.At(timebase.CodeData, off, "data chunk not found")
}

// ReadFrame fills dst with exactly one interleaved PCM frame. dst must have
// length BlockAlign. At the normal end it returns io.EOF.
func (r *Reader) ReadFrame(dst []byte) error {
	if len(dst) != r.meta.BlockAlign {
		return timebase.NewError(timebase.CodeInvalidArgument, "frame buffer has wrong block alignment")
	}
	_, err := io.ReadFull(r.data, dst)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.EOF
	}
	if err != nil {
		return timebase.At(timebase.CodeData, r.meta.DataOffset+r.framePos*int64(r.meta.BlockAlign),
			"read frame %d: %v", r.framePos, err)
	}
	r.framePos++
	return nil
}

// NextFrame returns a fresh frame byte slice. Callers may retain it; each call
// allocates at most the 32-byte maximum frame.
func (r *Reader) NextFrame() ([]byte, error) {
	b := make([]byte, r.meta.BlockAlign)
	if err := r.ReadFrame(b); err != nil {
		return nil, err
	}
	return b, nil
}

// DecodeSample sign-extends one little-endian PCM sample to left-aligned int32.
func DecodeSample(raw []byte, bits int) (int32, error) {
	switch bits {
	case 16:
		return int32(int16(binary.LittleEndian.Uint16(raw))) << 16, nil
	case 24:
		v := int32(raw[0]) | int32(raw[1])<<8 | int32(raw[2])<<16
		if v&0x800000 != 0 {
			v -= 1 << 24
		}
		return v << 8, nil
	case 32:
		return int32(binary.LittleEndian.Uint32(raw)), nil
	default:
		return 0, timebase.NewError(timebase.CodeFmt, "unsupported bits")
	}
}
