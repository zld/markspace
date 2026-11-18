// Package box implements low-level ISO BMFF box boundary reading and
// validation. It supports 32-bit, 64-bit and size=0 box lengths and
// rejects out-of-range, overflowing, truncated or illegally nested boxes.
// Every error carries the box type and file offset.
package box

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// MaxPayload bounds a single box payload read into memory (64 MiB).
const MaxPayload = 64 << 20

// Box describes the extent of one box in the file.
type Box struct {
	Type     string // 4-character box type
	Offset   int64  // absolute file offset of the box header
	Size     int64  // total size in bytes, header included
	HeadSize int64  // header size in bytes (8 or 16)
}

// DataOffset returns the absolute offset of the box payload.
func (b Box) DataOffset() int64 { return b.Offset + b.HeadSize }

// DataSize returns the payload size in bytes.
func (b Box) DataSize() int64 { return b.Size - b.HeadSize }

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "mvex": true,
	"moof": true, "traf": true, "mfra": true, "skip": true,
}

// IsContainer reports whether boxes of this type nest child boxes.
func IsContainer(t string) bool { return containerTypes[t] }

// Reader reads box boundaries from an io.ReaderAt with a known total size.
type Reader struct {
	r    io.ReaderAt
	size int64
}

// NewReader builds a Reader over r, whose total length is size bytes.
func NewReader(r io.ReaderAt, size int64) *Reader { return &Reader{r: r, size: size} }

// Size returns the total file size the reader was built with.
func (rd *Reader) Size() int64 { return rd.size }

// ReadAt exposes bounded payload reads (e.g. a single located sample).
func (rd *Reader) ReadAt(p []byte, off int64) (int, error) { return rd.r.ReadAt(p, off) }

// At reads the box header at offset off; the box must end at or before limit.
func (rd *Reader) At(off, limit int64) (Box, error) {
	if off < 0 || limit > rd.size || off > limit {
		return Box{}, fmt.Errorf("box at offset %d: invalid range (limit=%d file=%d)", off, limit, rd.size)
	}
	var hdr [16]byte
	n, _ := rd.r.ReadAt(hdr[:], off)
	if n < 8 {
		return Box{}, fmt.Errorf("box at offset %d: truncated header (%d of 8 bytes)", off, n)
	}
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	typ := string(hdr[4:8])
	for _, c := range []byte(typ) {
		if c < 0x20 || c > 0x7e {
			return Box{}, fmt.Errorf("box at offset %d: non-printable type bytes %q", off, typ)
		}
	}
	b := Box{Type: typ, Offset: off, HeadSize: 8}
	switch size32 {
	case 0: // box extends to the end of the enclosing scope
		b.Size = limit - off
	case 1: // 64-bit largesize
		if n < 16 {
			return Box{}, fmt.Errorf("box %q at offset %d: truncated 64-bit header", typ, off)
		}
		large := binary.BigEndian.Uint64(hdr[8:16])
		if large > math.MaxInt64 {
			return Box{}, fmt.Errorf("box %q at offset %d: 64-bit size %d overflows int64", typ, off, large)
		}
		b.Size = int64(large)
		b.HeadSize = 16
	default:
		b.Size = int64(size32)
	}
	if b.Size < b.HeadSize {
		return Box{}, fmt.Errorf("box %q at offset %d: size %d smaller than header %d", typ, off, b.Size, b.HeadSize)
	}
	if b.Size > limit-off {
		return Box{}, fmt.Errorf("box %q at offset %d: size %d exceeds enclosing bound %d", typ, off, b.Size, limit-off)
	}
	return b, nil
}

// Children lists the child boxes of a container box.
func (rd *Reader) Children(parent Box) ([]Box, error) {
	if !IsContainer(parent.Type) {
		return nil, fmt.Errorf("box %q at offset %d: not a container, cannot nest children", parent.Type, parent.Offset)
	}
	return rd.walk(parent.DataOffset(), parent.Offset+parent.Size)
}

// TopLevel lists the top-level boxes of the file.
func (rd *Reader) TopLevel() ([]Box, error) { return rd.walk(0, rd.size) }

func (rd *Reader) walk(start, end int64) ([]Box, error) {
	var out []Box
	for off := start; off < end; {
		b, err := rd.At(off, end)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		off += b.Size
		if off > end {
			return nil, fmt.Errorf("box %q at offset %d: child overruns parent end %d", b.Type, b.Offset, end)
		}
	}
	return out, nil
}

// ReadData reads the whole payload of b into a bounded buffer.
func (rd *Reader) ReadData(b Box) ([]byte, error) {
	if b.DataSize() > MaxPayload {
		return nil, fmt.Errorf("box %q at offset %d: payload %d exceeds %d byte limit", b.Type, b.Offset, b.DataSize(), MaxPayload)
	}
	buf := make([]byte, b.DataSize())
	if _, err := rd.r.ReadAt(buf, b.DataOffset()); err != nil {
		return nil, fmt.Errorf("box %q at offset %d: read payload: %w", b.Type, b.Offset, err)
	}
	return buf, nil
}
