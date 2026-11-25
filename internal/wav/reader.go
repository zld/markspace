// Package wav parses RIFF/WAVE PCM headers without trusting them.
//
// The reader never pads, extends or repairs a damaged file: it reports the
// first offset that cannot be interpreted, together with the media time that
// offset corresponds to. Callers decide whether a truncated tail is still
// usable. All multi-channel PCM widths supported by WAVE (8, 16, 24, 32-bit
// integer and 32/64-bit float) are described; only interleaved frames are
// exposed, one sample per channel per frame.
package wav

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"replaycheck/internal/clock"
	"replaycheck/internal/fault"
)

// MaxChannels is a sanity bound for a single task. It keeps per-frame
// allocations bounded while still covering multichannel PCM programmes.
const MaxChannels = 64

// MaxReadBytes bounds a single ReadFrames call so a task cannot accidentally
// materialise a whole programme into memory.
const MaxReadBytes = 8 << 20

// Format describes the fmt chunk.
type Format struct {
	AudioFormat uint16
	Channels    int
	SampleRate  int
	BitsPerSam  int
	BlockAlign  int
	ByteRate    int
}

// FrameBytes is bytes in one interleaved sample frame.
func (f Format) FrameBytes() int { return f.BlockAlign }

// Chunk is a chunk header located during parsing.
type Chunk struct {
	ID         string
	Offset     int64 // offset of the 8-byte header
	DataOffset int64
	Size       int64
}

// Layout is the parse result. DataSize is the size declared by the file;
// Available is the number of data bytes actually present.
type Layout struct {
	Format      Format
	Chunks      []Chunk
	DataOffset  int64
	DataSize    int64
	Available   int64
	Frames      int64 // complete frames available in the data chunk
	Truncated   bool
	PartialTail int // bytes of an incomplete trailing sample frame
	Issues      fault.List

	// FirstBadOffset is the first byte position that could not be
	// interpreted, or -1 when the whole file parsed cleanly.
	FirstBadOffset int64
}

// Timeline returns a timeline whose tick rate equals the sample rate, the
// natural time base for PCM.
func (l *Layout) Timeline() *clock.Timeline { return clock.NewForSampleRate(l.Format.SampleRate) }

// FrameTick returns the tick of a frame index.
func (l *Layout) FrameTick(frame int64) int64 { return frame }

// Peak scans complete frames [start, start+count) and returns the largest
// absolute normalised sample value (0..1). Reads in bounded blocks.
func (l *Layout) Peak(r io.ReaderAt, start, count int64) (float64, error) {
	if err := l.checkFrames(start, count); err != nil {
		return 0, err
	}
	const blockFrames = 4096
	fb := int64(l.Format.FrameBytes())
	if int64(blockFrames)*fb > MaxReadBytes {
		return 0, fmt.Errorf("wav: frame block of %d frames exceeds read cap", blockFrames)
	}
	buf := make([]byte, int64(blockFrames)*fb)
	var peak float64
	for done := int64(0); done < count; {
		n := count - done
		if n > blockFrames {
			n = blockFrames
		}
		off := l.DataOffset + (start+done)*fb
		if _, err := r.ReadAt(buf[:n*fb], off); err != nil {
			return 0, err
		}
		if p := peakInterleaved(buf[:n*fb], l.Format); p > peak {
			peak = p
		}
		done += n
	}
	return peak, nil
}

// ReadFrames returns interleaved bytes for complete frames [start,start+count).
func (l *Layout) ReadFrames(r io.ReaderAt, start, count int64) ([]byte, error) {
	if err := l.checkFrames(start, count); err != nil {
		return nil, err
	}
	nbytes := count * int64(l.Format.FrameBytes())
	if nbytes > MaxReadBytes {
		return nil, fmt.Errorf("wav: requested %d bytes exceeds %d byte cap", nbytes, MaxReadBytes)
	}
	buf := make([]byte, nbytes)
	if _, err := r.ReadAt(buf, l.DataOffset+start*int64(l.Format.FrameBytes())); err != nil {
		return nil, err
	}
	return buf, nil
}

func (l *Layout) checkFrames(start, count int64) error {
	if start < 0 || count < 0 || start+count > l.Frames {
		return fault.New(fault.KindOutOfRange, fault.Fatal, fault.ByteAt(l.dataEnd()),
			"frame range [%d,%d) outside %d complete frames", start, start+count, l.Frames)
	}
	return nil
}

func (l *Layout) dataEnd() int64 { return l.DataOffset + l.Available }

// Reader is an opened WAV file with a parsed layout.
type Reader struct {
	f      io.ReaderAt
	closer io.Closer
	Layout *Layout
}

// Open parses path and returns a reader positioned for frame access.
func Open(path string) (*Reader, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	lay, err := Parse(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Reader{f: f, closer: f, Layout: lay}, nil
}

// NewReader wraps an arbitrary ReaderAt of known size.
func NewReader(r io.ReaderAt, size int64) (*Reader, error) {
	lay, err := Parse(r, size)
	if err != nil {
		return nil, err
	}
	return &Reader{f: r, Layout: lay}, nil
}

// Close releases the underlying file, if any.
func (r *Reader) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

// ReadFrames proxies to Layout.ReadFrames.
func (r *Reader) ReadFrames(start, count int64) ([]byte, error) {
	return r.Layout.ReadFrames(r.f, start, count)
}

// Peak proxies to Layout.Peak.
func (r *Reader) Peak(start, count int64) (float64, error) {
	return r.Layout.Peak(r.f, start, count)
}

// Parse reads a RIFF/WAVE structure from r without modifying it.
func Parse(r io.ReaderAt, size int64) (*Layout, error) {
	if size < 12 {
		return nil, fault.New(fault.KindTruncated, fault.Fatal, fault.ByteAt(size),
			"file of %d bytes too short for RIFF header", size)
	}
	head := make([]byte, 12)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return nil, fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(0),
			"not a RIFF/WAVE file (magic %q/%q)", head[0:4], head[8:12])
	}
	declaredRIFF := int64(binary.LittleEndian.Uint32(head[4:8])) + 8
	if declaredRIFF < 12 {
		return nil, fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(4),
			"RIFF size %d smaller than header", declaredRIFF)
	}
	limit := size
	// A RIFF header that promises more bytes than the file holds means the
	// file was cut short; the chunks that are present stay parseable, and
	// the missing tail is reported at the file's end.
	riffShort := declaredRIFF > size
	if declaredRIFF < limit {
		limit = declaredRIFF
	}

	lay := &Layout{FirstBadOffset: -1}
	lay.Issues = fault.List{}
	var fmtSeen, dataSeen bool
	pos := int64(12)
	for pos+8 <= limit {
		var hdr [8]byte
		if _, err := r.ReadAt(hdr[:], pos); err != nil {
			return nil, err
		}
		id := string(hdr[0:4])
		csize := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		dataOff := pos + 8
		if csize < 0 || dataOff > limit || csize > limit-dataOff {
			// The chunk claims more bytes than the file holds.
			avail := limit - dataOff
			if avail < 0 {
				avail = 0
			}
			ch := Chunk{ID: id, Offset: pos, DataOffset: dataOff, Size: csize}
			lay.Chunks = append(lay.Chunks, ch)
			bad := dataOff + avail
			lay.FirstBadOffset = bad
			lay.Issues.Add(fault.New(fault.KindTruncated, fault.Damaged, fault.ByteAt(bad),
				"chunk %q declares %d bytes, only %d present", id, csize, avail))
			if id == "data" {
				lay.DataOffset, lay.DataSize, dataSeen = dataOff, csize, true
				lay.Available = avail
				lay.Truncated = true
			}
			pos = limit
			break
		}
		lay.Chunks = append(lay.Chunks, Chunk{ID: id, Offset: pos, DataOffset: dataOff, Size: csize})
		switch id {
		case "fmt ":
			if fmtSeen {
				lay.Issues.Add(fault.New(fault.KindBadChunk, fault.Damaged, fault.ByteAt(pos),
					"duplicate fmt chunk"))
			} else if err := parseFmt(r, lay, dataOff, csize); err != nil {
				return nil, err
			}
			fmtSeen = true
		case "data":
			if dataSeen {
				lay.Issues.Add(fault.New(fault.KindBadChunk, fault.Damaged, fault.ByteAt(pos),
					"duplicate data chunk"))
			}
			lay.DataOffset, lay.DataSize, dataSeen = dataOff, csize, true
			lay.Available = csize
		}
		next := dataOff + csize
		if csize%2 == 1 {
			next++
		}
		if next <= pos {
			return nil, fault.New(fault.KindBadChunk, fault.Fatal, fault.ByteAt(pos),
				"chunk %q at %d has non-advancing size", id, pos)
		}
		pos = next
	}

	if riffShort {
		lay.Issues.Add(fault.New(fault.KindTruncated, fault.Damaged, fault.ByteAt(size),
			"RIFF declares %d bytes, file holds %d", declaredRIFF, size))
		lay.Truncated = true
		if lay.FirstBadOffset < 0 {
			lay.FirstBadOffset = size
		}
	}
	if !fmtSeen {
		return nil, fault.New(fault.KindBadChunk, fault.Fatal, fault.ByteAt(12),
			"no fmt chunk found")
	}
	if !dataSeen {
		return nil, fault.New(fault.KindBadChunk, fault.Fatal, fault.ByteAt(limit),
			"no data chunk found")
	}

	fb := int64(lay.Format.BlockAlign)
	lay.Frames = lay.Available / fb
	lay.PartialTail = int(lay.Available % fb)
	if lay.PartialTail != 0 {
		off := lay.DataOffset + lay.Frames*fb
		tl := lay.Timeline()
		lay.Issues.Add(fault.New(fault.KindPartialFrame, fault.Damaged,
			fault.Location{ByteOffset: off, Tick: lay.Frames, HasTick: true,
				Millis: tl.TicksToMillis(lay.Frames), Label: "data"},
			"trailing %d bytes do not form a complete %d-channel frame", lay.PartialTail, lay.Format.Channels))
		lay.Truncated = true
		if lay.FirstBadOffset < 0 || off < lay.FirstBadOffset {
			lay.FirstBadOffset = off
		}
	}
	return lay, nil
}

func parseFmt(r io.ReaderAt, lay *Layout, off, size int64) error {
	if size < 16 {
		return fault.New(fault.KindBadChunk, fault.Fatal, fault.ByteAt(off),
			"fmt chunk is %d bytes, need at least 16", size)
	}
	n := size
	if n > 40 {
		n = 40
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, off); err != nil {
		return err
	}
	f := Format{
		AudioFormat: binary.LittleEndian.Uint16(buf[0:2]),
		Channels:    int(binary.LittleEndian.Uint16(buf[2:4])),
		SampleRate:  int(binary.LittleEndian.Uint32(buf[4:8])),
		ByteRate:    int(binary.LittleEndian.Uint32(buf[8:12])),
		BlockAlign:  int(binary.LittleEndian.Uint16(buf[12:14])),
		BitsPerSam:  int(binary.LittleEndian.Uint16(buf[14:16])),
	}
	if f.AudioFormat == 0xFFFE {
		if size < 40 {
			return fault.New(fault.KindUnsupported, fault.Fatal, fault.ByteAt(off),
				"WAVE_FORMAT_EXTENSIBLE fmt chunk too short")
		}
		f.AudioFormat = binary.LittleEndian.Uint16(buf[24:26]) // sub-format GUID prefix
	}
	if f.AudioFormat != 1 && f.AudioFormat != 3 {
		return fault.New(fault.KindUnsupported, fault.Fatal, fault.ByteAt(off),
			"unsupported audio format %#x (want PCM 1 or IEEE float 3)", f.AudioFormat)
	}
	if f.Channels < 1 || f.Channels > MaxChannels {
		return fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(off+2),
			"channel count %d outside 1..%d", f.Channels, MaxChannels)
	}
	if f.SampleRate < 1 || f.SampleRate > 768000 {
		return fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(off+4),
			"sample rate %d outside 1..768000", f.SampleRate)
	}
	if f.BitsPerSam == 0 || f.BitsPerSam%8 != 0 || f.BitsPerSam > 64 {
		return fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(off+14),
			"bits per sample %d is not a byte multiple <= 64", f.BitsPerSam)
	}
	if f.AudioFormat == 1 && f.BitsPerSam > 32 {
		return fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(off+14),
			"integer PCM depth %d exceeds 32 bits", f.BitsPerSam)
	}
	wantAlign := f.Channels * f.BitsPerSam / 8
	if f.BlockAlign != wantAlign {
		return fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(off+12),
			"block align %d inconsistent with %d channels x %d bits (want %d)",
			f.BlockAlign, f.Channels, f.BitsPerSam, wantAlign)
	}
	if f.ByteRate != f.SampleRate*f.BlockAlign {
		return fault.New(fault.KindBadHeader, fault.Fatal, fault.ByteAt(off+8),
			"byte rate %d inconsistent with %d Hz x %d bytes (want %d)",
			f.ByteRate, f.SampleRate, f.BlockAlign, f.SampleRate*f.BlockAlign)
	}
	lay.Format = f
	return nil
}

func peakInterleaved(b []byte, f Format) float64 {
	var peak float64
	width := f.BitsPerSam / 8
	switch {
	case f.AudioFormat == 3 && width == 4:
		for i := 0; i+4 <= len(b); i += 4 {
			bits := binary.LittleEndian.Uint32(b[i : i+4])
			v := float64(math.Float32frombits(bits))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
	case f.AudioFormat == 3 && width == 8:
		for i := 0; i+8 <= len(b); i += 8 {
			bits := binary.LittleEndian.Uint64(b[i : i+8])
			v := math.Float64frombits(bits)
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
	case width == 1:
		for i, v := range b {
			_ = i
			d := float64(int(v)-128) / 128
			if d < 0 {
				d = -d
			}
			if d > peak {
				peak = d
			}
		}
	case width == 2:
		for i := 0; i+2 <= len(b); i += 2 {
			d := float64(int16(binary.LittleEndian.Uint16(b[i:i+2]))) / 32768
			if d < 0 {
				d = -d
			}
			if d > peak {
				peak = d
			}
		}
	case width == 3:
		for i := 0; i+3 <= len(b); i += 3 {
			u := uint32(b[i])<<8 | uint32(b[i+1])<<16 | uint32(b[i+2])<<24
			d := float64(int32(u)>>8) / 8388608
			if d < 0 {
				d = -d
			}
			if d > peak {
				peak = d
			}
		}
	case width == 4:
		for i := 0; i+4 <= len(b); i += 4 {
			d := float64(int32(binary.LittleEndian.Uint32(b[i:i+4]))) / 2147483648
			if d < 0 {
				d = -d
			}
			if d > peak {
				peak = d
			}
		}
	}
	return peak
}
