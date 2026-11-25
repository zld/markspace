// Package wav parses uncompressed PCM/WAV streams and validates their
// channel layout. Decoding never repairs data: a truncated header or a
// data chunk that does not contain a whole number of frames is reported
// with a distinct categorized error and no indexable output is produced.
package wav

import (
	"bytes"
	"encoding/binary"
	"io"

	"continuity/internal/media"
)

// Sentinels are wrapped inside categorized media errors so callers can
// both inspect the category (media.CategoryOf) and use errors.Is.
var (
	ErrTruncatedHeader = media.NewError(media.CatInvalidInput, "wav: truncated or malformed RIFF header")
	ErrPartialFrame    = media.NewError(media.CatInvalidInput, "wav: data chunk is not a whole number of frames")
	ErrUnsupported     = media.NewError(media.CatInvalidInput, "wav: unsupported format")
	ErrNoDataChunk     = media.NewError(media.CatInvalidInput, "wav: missing data chunk")
)

const (
	headerRiff = 12 // "RIFF" size "WAVE"
	chunkHdr   = 8  // fourcc + uint32 size
)

// File is a parsed WAV file. PCM bytes stay on disk; callers read frames
// through ReadFrames instead of holding the whole stream in memory.
type File struct {
	SampleRate   uint32
	Channels     uint16
	BitsPerSample uint16
	BlockAlign   uint16
	DataOffset   int64
	DataLength   int64 // bytes, always a multiple of BlockAlign after Parse
	FrameCount   int64
	r            io.ReaderAt
}

// Parse validates the RIFF/WAVE structure and fmt chunk of r. Only PCM
// (format tag 1) at 8, 16 or 24 bits per sample is accepted. The reader
// is retained for later frame reads.
func Parse(r io.ReadSeeker) (*File, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, media.WrapError(media.CatIO, "wav: seek failed", err)
	}
	var head [headerRiff]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, media.WrapError(media.CatInvalidInput, "wav: truncated RIFF header", err)
	}
	if !bytes.Equal(head[0:4], []byte("RIFF")) || !bytes.Equal(head[8:12], []byte("WAVE")) {
		return nil, ErrTruncatedHeader
	}

	f := &File{}
	var fmtData []byte
	var dataOff, dataLen int64
	foundData := false
	for {
		var ch [chunkHdr]byte
		n, err := io.ReadFull(r, ch[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n != 0 {
				return nil, ErrTruncatedHeader
			}
			break
		}
		if err != nil {
			return nil, media.WrapError(media.CatIO, "wav: chunk read failed", err)
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		pos, _ := r.Seek(0, io.SeekCurrent)
		switch id {
		case "fmt ":
			fmtData = make([]byte, size)
			if _, err := io.ReadFull(r, fmtData); err != nil {
				return nil, ErrTruncatedHeader
			}
		case "data":
			dataOff, dataLen = pos, size
			foundData = true
			if err := skip(r, size); err != nil {
				return nil, media.WrapError(media.CatIO, "wav: skip data failed", err)
			}
		default:
			if err := skip(r, size); err != nil {
				return nil, media.WrapError(media.CatIO, "wav: skip chunk failed", err)
			}
		}
		// Chunks are word aligned with one padding byte when size is odd.
		if size%2 == 1 {
			if err := skip(r, 1); err != nil {
				return nil, media.WrapError(media.CatIO, "wav: skip pad failed", err)
			}
		}
	}
	if fmtData == nil {
		return nil, media.WrapError(media.CatInvalidInput, "wav: missing fmt chunk", nil)
	}
	if err := parseFmt(fmtData, f); err != nil {
		return nil, err
	}
	if !foundData {
		return nil, ErrNoDataChunk
	}
	if dataLen%int64(f.BlockAlign) != 0 {
		return nil, ErrPartialFrame
	}
	ra, ok := r.(io.ReaderAt)
	if !ok {
		return nil, media.NewError(media.CatUnrecoverable, "wav: reader does not support random access")
	}
	// A header may promise more data bytes than the file actually holds;
	// treat that as a truncated input rather than silently dropping frames.
	probe := make([]byte, 1)
	if _, err := ra.ReadAt(probe, dataOff+dataLen-1); err != nil {
		return nil, media.WrapError(media.CatInvalidInput, "wav: declared data chunk exceeds file size", err)
	}
	f.DataOffset, f.DataLength = dataOff, dataLen
	f.FrameCount = dataLen / int64(f.BlockAlign)
	f.r = ra
	return f, nil
}

func parseFmt(b []byte, f *File) error {
	if len(b) < 16 {
		return ErrTruncatedHeader
	}
	format := binary.LittleEndian.Uint16(b[0:2])
	if format != 1 {
		return media.WrapError(media.CatInvalidInput, "wav: non-PCM format tag", ErrUnsupported)
	}
	f.Channels = binary.LittleEndian.Uint16(b[2:4])
	f.SampleRate = binary.LittleEndian.Uint32(b[4:8])
	byteRate := binary.LittleEndian.Uint32(b[8:12])
	f.BlockAlign = binary.LittleEndian.Uint16(b[12:14])
	f.BitsPerSample = binary.LittleEndian.Uint16(b[14:16])
	if f.Channels == 0 || f.SampleRate == 0 || f.BitsPerSample == 0 {
		return media.WrapError(media.CatInvalidInput, "wav: zero-valued fmt field", nil)
	}
	switch f.BitsPerSample {
	case 8, 16, 24:
	default:
		return media.WrapError(media.CatInvalidInput, "wav: unsupported bit depth", ErrUnsupported)
	}
	if want := f.Channels * f.BitsPerSample / 8; f.BlockAlign != want {
		return media.WrapError(media.CatLayout, "wav: block align does not match channels and bit depth", nil)
	}
	if want := f.SampleRate * uint32(f.BlockAlign); byteRate != want {
		return media.WrapError(media.CatLayout, "wav: byte rate does not match sample rate and block align", nil)
	}
	return nil
}

func skip(r io.Seeker, n int64) error {
	_, err := r.Seek(n, io.SeekCurrent)
	return err
}

// ReadFrames reads up to len(dst)/BlockAlign whole frames starting at
// startFrame. It returns the number of frames read; io.EOF is returned
// when no frame could be read because the stream ended.
func (f *File) ReadFrames(dst []byte, startFrame int64) (int, error) {
	if startFrame < 0 || startFrame > f.FrameCount {
		return 0, media.NewError(media.CatInvalidInput, "wav: frame offset out of range")
	}
	maxBytes := f.DataLength - startFrame*int64(f.BlockAlign)
	if maxBytes <= 0 {
		return 0, io.EOF
	}
	want := int64(len(dst))
	if want > maxBytes {
		want = maxBytes
	}
	want -= want % int64(f.BlockAlign)
	if want == 0 {
		return 0, nil
	}
	off := f.DataOffset + startFrame*int64(f.BlockAlign)
	if _, err := f.r.ReadAt(dst[:want], off); err != nil && err != io.EOF {
		return 0, media.WrapError(media.CatIO, "wav: frame read failed", err)
	}
	return int(want / int64(f.BlockAlign)), nil
}
