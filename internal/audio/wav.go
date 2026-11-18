// Package audio parses PCM WAV files and synthesizes deterministic
// test fixtures. Only local files are read; nothing is executed.
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// MaxFileSize bounds accepted input files (2 GiB).
const MaxFileSize = 2 << 30

// Audio format tags supported by the parser.
const (
	FormatPCM   = 1
	FormatFloat = 3
)

// Info describes a parsed WAV stream.
type Info struct {
	SampleRate int
	Channels   int
	BitDepth   int
	Format     int
	Frames     int64
}

// Reader streams deinterleaved samples from a WAV file.
type Reader struct {
	f          *os.File
	info       Info
	blockAlign int
	dataOff    int64
	dataLeft   int64
	buf        []byte
}

// Info returns the parsed stream description.
func (r *Reader) Info() Info { return r.info }

// Close releases the underlying file.
func (r *Reader) Close() error { return r.f.Close() }

// Open parses the WAV header of path and returns a streaming Reader.
func Open(path string) (*Reader, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if fi.IsDir() {
		return nil, errors.New("is a directory")
	}
	if fi.Size() > MaxFileSize {
		return nil, fmt.Errorf("file exceeds 2 GiB limit (%d bytes)", fi.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := &Reader{f: f}
	if err := r.parseHeader(fi.Size()); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func skipChunk(f *os.File, size int64) error {
	pad := size & 1
	_, err := f.Seek(size+pad, io.SeekCurrent)
	return err
}

func (r *Reader) parseHeader(fileSize int64) error {
	var riff [12]byte
	if _, err := io.ReadFull(r.f, riff[:]); err != nil {
		return fmt.Errorf("reading RIFF header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return errors.New("not a RIFF/WAVE file")
	}
	var fmtBuf [16]byte
	var haveFmt, haveData bool
	var dataSize int64
	for !(haveFmt && haveData) {
		var ch [8]byte
		if _, err := io.ReadFull(r.f, ch[:]); err != nil {
			return fmt.Errorf("reading chunk header: %w", err)
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch string(ch[0:4]) {
		case "fmt ":
			if size < 16 {
				return fmt.Errorf("fmt chunk too small (%d bytes)", size)
			}
			if _, err := io.ReadFull(r.f, fmtBuf[:]); err != nil {
				return fmt.Errorf("reading fmt chunk: %w", err)
			}
			haveFmt = true
			if err := skipChunk(r.f, size-16); err != nil {
				return err
			}
		case "data":
			off, err := r.f.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			r.dataOff, dataSize, haveData = off, size, true
			if !haveFmt {
				if err := skipChunk(r.f, size); err != nil {
					return err
				}
			}
		default:
			if err := skipChunk(r.f, size); err != nil {
				return err
			}
		}
	}
	if !haveFmt {
		return errors.New("missing fmt chunk")
	}
	if !haveData {
		return errors.New("missing data chunk")
	}
	format := int(binary.LittleEndian.Uint16(fmtBuf[0:2]))
	channels := int(binary.LittleEndian.Uint16(fmtBuf[2:4]))
	sampleRate := int(binary.LittleEndian.Uint32(fmtBuf[4:8]))
	blockAlign := int(binary.LittleEndian.Uint16(fmtBuf[12:14]))
	bitDepth := int(binary.LittleEndian.Uint16(fmtBuf[14:16]))
	if format != FormatPCM && format != FormatFloat {
		return fmt.Errorf("unsupported encoding (format tag %d)", format)
	}
	if channels < 1 {
		return errors.New("zero channels")
	}
	if sampleRate <= 0 {
		return errors.New("invalid sample rate")
	}
	switch bitDepth {
	case 16, 24, 32:
	default:
		return fmt.Errorf("unsupported bit depth %d", bitDepth)
	}
	if format == FormatFloat && bitDepth != 32 {
		return fmt.Errorf("float WAV must be 32-bit, got %d", bitDepth)
	}
	if want := channels * bitDepth / 8; blockAlign != want {
		return fmt.Errorf("block align %d does not match %d channels x %d bits",
			blockAlign, channels, bitDepth)
	}
	if dataSize%int64(blockAlign) != 0 {
		return errors.New("data chunk not aligned to whole frames")
	}
	if r.dataOff+dataSize > fileSize {
		return errors.New("data chunk truncated")
	}
	if _, err := r.f.Seek(r.dataOff, io.SeekStart); err != nil {
		return err
	}
	r.info = Info{SampleRate: sampleRate, Channels: channels, BitDepth: bitDepth,
		Format: format, Frames: dataSize / int64(blockAlign)}
	r.blockAlign = blockAlign
	r.dataLeft = dataSize
	return nil
}

// ReadBlock deinterleaves up to frames frames into per-channel float64
// slices normalized so full scale is 1.0. It returns nil at end of stream.
func (r *Reader) ReadBlock(frames int) ([][]float64, error) {
	if r.dataLeft == 0 {
		return nil, nil
	}
	if avail := r.dataLeft / int64(r.blockAlign); int64(frames) > avail {
		frames = int(avail)
	}
	need := frames * r.blockAlign
	if cap(r.buf) < need {
		r.buf = make([]byte, need)
	}
	if _, err := io.ReadFull(r.f, r.buf[:need]); err != nil {
		return nil, fmt.Errorf("reading samples: %w", err)
	}
	ch := r.info.Channels
	out := make([][]float64, ch)
	for c := range out {
		out[c] = make([]float64, frames)
	}
	switch {
	case r.info.Format == FormatFloat:
		for i := 0; i < frames; i++ {
			for c := 0; c < ch; c++ {
				bits := binary.LittleEndian.Uint32(r.buf[(i*ch+c)*4:])
				out[c][i] = float64(math.Float32frombits(bits))
			}
		}
	case r.info.BitDepth == 16:
		for i := 0; i < frames; i++ {
			for c := 0; c < ch; c++ {
				v := int16(binary.LittleEndian.Uint16(r.buf[(i*ch+c)*2:]))
				out[c][i] = float64(v) / 32768
			}
		}
	case r.info.BitDepth == 24:
		for i := 0; i < frames; i++ {
			for c := 0; c < ch; c++ {
				o := (i*ch + c) * 3
				v := int32(r.buf[o])<<8 | int32(r.buf[o+1])<<16 | int32(r.buf[o+2])<<24
				out[c][i] = float64(v>>8) / 8388608
			}
		}
	default: // 32-bit PCM
		for i := 0; i < frames; i++ {
			for c := 0; c < ch; c++ {
				v := int32(binary.LittleEndian.Uint32(r.buf[(i*ch+c)*4:]))
				out[c][i] = float64(v) / 2147483648
			}
		}
	}
	r.dataLeft -= int64(need)
	return out, nil
}
