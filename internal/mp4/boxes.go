// Package mp4 is the parsing-and-offset subsystem: it scans CMAF/fMP4 box
// structures with byte-accurate locations and decodes the fragment boxes
// (mfhd/tfhd/tfdt/trun) plus media-data ranges. Every malformed input is
// reported as a *timeline.MediaError carrying file, byte offset and code.
package mp4

import (
	"bytes"
	"encoding/binary"
	"io"

	"mediad/internal/timeline"
)

// Limits bounds untrusted input. Zero fields fall back to DefaultLimits.
type Limits struct {
	MaxFileSize int64 // maximum total input bytes
	MaxBoxSize  int64 // maximum single declared box size
	MaxSamples  int   // maximum samples declared by one trun
}

// DefaultLimits are conservative caps suitable for local demo fixtures.
func DefaultLimits() Limits {
	return Limits{MaxFileSize: 256 << 20, MaxBoxSize: 128 << 20, MaxSamples: 1_000_000}
}

// Box is one ISO/IEC 14496-12 box with its absolute location in the file.
type Box struct {
	Type       string // four-character type, e.g. "moof"
	Offset     int64  // absolute offset of the box header
	HeaderSize int64  // 8 (small) or 16 (largesize)
	Size       int64  // total size including header (resolved even when size==0)
	PayloadOff int64  // absolute offset of the first payload byte
	PayloadLen int64  // payload length
	Children   []*Box // populated for container boxes
}

// End returns the absolute offset just past the box.
func (b *Box) End() int64 { return b.Offset + b.Size }

// FindChild returns the first direct child of the given type, or nil.
func (b *Box) FindChild(typ string) *Box {
	for _, c := range b.Children {
		if c.Type == typ {
			return c
		}
	}
	return nil
}

// FindPath walks a sequence of child box types from the receiver.
func (b *Box) FindPath(types ...string) *Box {
	cur := b
	for _, t := range types {
		cur = cur.FindChild(t)
		if cur == nil {
			return nil
		}
	}
	return cur
}

var containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
	"edts": true, "dinf": true, "moof": true, "traf": true, "mvex": true,
}

func (l Limits) orDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFileSize <= 0 {
		l.MaxFileSize = d.MaxFileSize
	}
	if l.MaxBoxSize <= 0 {
		l.MaxBoxSize = d.MaxBoxSize
	}
	if l.MaxSamples <= 0 {
		l.MaxSamples = d.MaxSamples
	}
	return l
}

// ScanTopLevel scans every top-level box of an fMP4 file.
func ScanTopLevel(r io.ReadSeeker, file string, lim Limits) ([]*Box, error) {
	lim = lim.orDefaults()
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if end > lim.MaxFileSize {
		return nil, timeline.New(timeline.ErrLimitExceeded, file, 0,
			"input size %d exceeds limit %d", end, lim.MaxFileSize)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return scanRange(r, file, 0, end, lim)
}

func scanRange(r io.ReadSeeker, file string, start, end int64, lim Limits) ([]*Box, error) {
	var boxes []*Box
	off := start
	for off < end {
		b, next, err := readBoxHeader(r, file, off, end, lim)
		if err != nil {
			return boxes, err
		}
		if containers[b.Type] {
			kids, err := scanRange(r, file, b.PayloadOff, b.End(), lim)
			if err != nil {
				return boxes, err
			}
			b.Children = kids
		}
		boxes = append(boxes, b)
		off = next
	}
	return boxes, nil
}

func readBoxHeader(r io.ReadSeeker, file string, off, end int64, lim Limits) (*Box, int64, error) {
	var hdr [16]byte
	if off+8 > end {
		return nil, off, timeline.New(timeline.ErrBoxTruncated, file, off,
			"truncated box header: %d byte(s) of 8 available", end-off)
	}
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, off, err
	}
	if _, err := io.ReadFull(r, hdr[:8]); err != nil {
		return nil, off, timeline.New(timeline.ErrBoxTruncated, file, off,
			"cannot read box header: %v", err)
	}
	small := int64(binary.BigEndian.Uint32(hdr[:4]))
	typ := string(hdr[4:8])
	headerSize := int64(8)
	size := small
	switch small {
	case 0: // box extends to end of file
		size = end - off
	case 1: // 64-bit largesize follows
		if off+16 > end {
			return nil, off, timeline.New(timeline.ErrBoxTruncated, file, off+8,
				"truncated largesize: %d byte(s) of 8 available", end-(off+8))
		}
		if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
			return nil, off, timeline.New(timeline.ErrBoxTruncated, file, off+8,
				"cannot read largesize: %v", err)
		}
		large := int64(binary.BigEndian.Uint64(hdr[8:16]))
		if large < 16 {
			return nil, off, timeline.New(timeline.ErrBadBoxLength, file, off,
				"largesize %d smaller than 16-byte header", large)
		}
		headerSize, size = 16, large
	default:
		if small < 8 {
			return nil, off, timeline.New(timeline.ErrBadBoxLength, file, off,
				"declared box size %d smaller than 8-byte header", small)
		}
	}
	if size > lim.MaxBoxSize {
		return nil, off, timeline.New(timeline.ErrLimitExceeded, file, off,
			"box %q size %d exceeds limit %d", typ, size, lim.MaxBoxSize)
	}
	if off+size > end {
		// Report the first missing byte: the real cut position.
		return nil, off, timeline.New(timeline.ErrBoxTruncated, file, end,
			"box %q declares %d bytes but input ends at %d", typ, off+size, end)
	}
	b := &Box{
		Type:       typ,
		Offset:     off,
		HeaderSize: headerSize,
		Size:       size,
		PayloadOff: off + headerSize,
		PayloadLen: size - headerSize,
	}
	return b, off + size, nil
}

// ReadPayload returns the (bounded) payload bytes of a box.
func ReadPayload(r io.ReadSeeker, b *Box) ([]byte, error) {
	if b.PayloadLen < 0 {
		return nil, timeline.New(timeline.ErrBadBoxLength, "", b.Offset, "negative payload length")
	}
	buf := make([]byte, b.PayloadLen)
	if _, err := r.Seek(b.PayloadOff, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// NewSeeker wraps raw bytes as a seekable reader.
func NewSeeker(data []byte) io.ReadSeeker { return bytes.NewReader(data) }
