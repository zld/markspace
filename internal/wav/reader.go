// Package wav parses RIFF/WAVE files containing PCM or IEEE float samples.
//
// Files are parsed strictly and demultiplexed in bounded blocks: the reader
// never loads a whole file into memory, so analysis of long material has a
// fixed footprint regardless of duration.
//
// The reader is intentionally strict: chunk bounds, format conflicts and
// non-finite float samples are reported as file-level errors instead of being
// silently repaired, because a loudness measurement built on guessed data is
// worse than no measurement at all.
package wav

import (
	"errors"
	"fmt"
	"os"
)

// Format tags defined by the WAVE specification.
const (
	FormatPCM        uint16 = 1
	FormatFloat      uint16 = 3
	FormatExtensible uint16 = 0xFFFE
)

// subFormatSuffix is the fixed tail of the WAVE_FORMAT_EXTENSIBLE sub-format
// GUID: tag(4) 00 00 10 00 80 00 00 AA 00 38 9B 71.
var subFormatSuffix = [12]byte{0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}

var (
	// ErrTruncated indicates a chunk that extends past the end of the file.
	ErrTruncated = errors.New("truncated chunk")
	// ErrFormat indicates a WAVE encoding this tool does not support.
	ErrFormat = errors.New("unsupported format")
	// ErrNonFinite indicates a NaN or Inf sample in an IEEE float file.
	ErrNonFinite = errors.New("non-finite float sample")
)

// Info describes the sample data of a WAV file.
type Info struct {
	Format       uint16
	Channels     int
	SampleRate   int
	Bits         int
	Frames       int64
	DurationSecs float64
}

// header is the parsed RIFF/WAVE metadata.
type header struct {
	Format     uint16
	Channels   int
	SampleRate int
	Bits       int
	BlockAlign int
	DataOffset int64
	DataSize   int64
}

// Reader streams decoded samples from a WAV file.
type Reader struct {
	f         *os.File
	Info      Info
	header    header
	remaining int64
	consumed  int64
	buf       []byte
}

// Open parses the header of path and positions the reader at the sample data.
func Open(path string) (*Reader, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("wav: %s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}
	r := &Reader{f: f}
	if err := r.parseHeader(st.Size()); err != nil {
		f.Close()
		return nil, err
	}
	r.Info = Info{
		Format:     r.header.Format,
		Channels:   r.header.Channels,
		SampleRate: r.header.SampleRate,
		Bits:       r.header.Bits,
		Frames:     r.header.DataSize / int64(r.header.BlockAlign),
	}
	r.Info.DurationSecs = float64(r.Info.Frames) / float64(r.Info.SampleRate)
	r.remaining = r.header.DataSize
	return r, nil
}

// Close releases the underlying file handle.
func (r *Reader) Close() error { return r.f.Close() }
