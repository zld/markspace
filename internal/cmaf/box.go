// Package cmaf parses the subset of ISO BMFF boxes used by fMP4/CMAF
// initialization segments and moof/mdat fragments. Every read is bounds
// checked and every size arithmetic is guarded against integer overflow so a
// malformed or hostile input can never trigger an out-of-range slice.
package cmaf

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Safety caps keep a malformed input from forcing excessive allocations.
const (
	MaxBoxSize      = 16 << 20 // 16 MiB per single box
	MaxFragmentSize = 16 << 20 // 16 MiB per whole fragment
	headerSize      = 8
	largeHeaderSize = 16
)

var (
	// ErrTruncated means the buffer ended before a box or field completed.
	ErrTruncated = errors.New("cmaf: truncated box")
	// ErrOutOfBounds means a declared box size reaches past its container.
	ErrOutOfBounds = errors.New("cmaf: box out of bounds")
	// ErrOverflow means size arithmetic would overflow int64.
	ErrOverflow = errors.New("cmaf: integer overflow in box size")
	// ErrTooLarge means a box exceeds the configured safety cap.
	ErrTooLarge = errors.New("cmaf: box exceeds size cap")
)

// Box is one parsed ISO BMFF box. Data is a sub-slice of the original
// buffer (header excluded) and is always fully in bounds.
type Box struct {
	Type   string
	Start  int64 // absolute offset of the box header in the root buffer
	Header int   // header length: 8 or 16
	Size   int64 // total declared size including header
	Data   []byte
}

// End returns the absolute offset just past the box.
func (b Box) End() int64 { return b.Start + b.Size }

// ScanBoxes walks the boxes contained in data. A zero declared size means the
// box runs to the end of the buffer (only valid for the final box).
func ScanBoxes(data []byte) ([]Box, error) {
	var boxes []Box
	var pos int64
	for int(pos) < len(data) {
		b, next, err := scanOne(data, pos)
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, b)
		pos = next
	}
	return boxes, nil
}

func scanOne(data []byte, pos int64) (Box, int64, error) {
	if int(pos) < 0 || len(data)-int(pos) < headerSize {
		return Box{}, 0, ErrTruncated
	}
	hdr := data[pos:]
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	typ := string(hdr[4:8])
	header := int64(headerSize)
	size := int64(size32)
	if size32 == 1 {
		if len(data)-int(pos) < largeHeaderSize {
			return Box{}, 0, ErrTruncated
		}
		large := binary.BigEndian.Uint64(hdr[8:16])
		if large > uint64(1<<63-1) {
			return Box{}, 0, ErrOverflow
		}
		size = int64(large)
		header = largeHeaderSize
	}
	if size == 0 {
		size = int64(len(data)) - pos
	}
	if size < header {
		return Box{}, 0, fmt.Errorf("cmaf: box %q smaller than its header", typ)
	}
	if size > MaxBoxSize {
		return Box{}, 0, fmt.Errorf("%w: %s declares %d bytes", ErrTooLarge, typ, size)
	}
	end := pos + size
	if end < pos || end > int64(len(data)) {
		return Box{}, 0, fmt.Errorf("%w: %s reaches %d, buffer ends %d",
			ErrOutOfBounds, typ, end, len(data))
	}
	payload := data[pos+header : end]
	b := Box{Type: typ, Start: pos, Header: int(header), Size: size, Data: payload}
	return b, end, nil
}

// Find returns the first child box of the given type inside data, or nil.
func Find(data []byte, typ string) *Box {
	boxes, err := ScanBoxes(data)
	if err != nil {
		return nil
	}
	for i := range boxes {
		if boxes[i].Type == typ {
			return &boxes[i]
		}
	}
	return nil
}

// FindPath resolves a nested box path, e.g. FindPath(moof, "traf", "tfhd").
func FindPath(data []byte, path ...string) (*Box, error) {
	cur := data
	var b *Box
	for _, typ := range path {
		b = Find(cur, typ)
		if b == nil {
			return nil, fmt.Errorf("cmaf: required box %q not found", typ)
		}
		cur = b.Data
	}
	return b, nil
}

// FullHeader parses a FullBox (version + 24-bit flags), returning the payload
// past the four header bytes.
func (b Box) FullHeader() (version uint8, flags uint32, payload []byte, err error) {
	if len(b.Data) < 4 {
		return 0, 0, nil, ErrTruncated
	}
	version = b.Data[0]
	flags = uint32(b.Data[1])<<16 | uint32(b.Data[2])<<8 | uint32(b.Data[3])
	return version, flags, b.Data[4:], nil
}

// U16 reads a big-endian uint16 at off, rejecting out-of-range access.
func U16(data []byte, off int) (uint16, error) {
	if off < 0 || len(data)-off < 2 {
		return 0, ErrTruncated
	}
	return binary.BigEndian.Uint16(data[off : off+2]), nil
}

// U32 reads a big-endian uint32 at off, rejecting out-of-range access.
func U32(data []byte, off int) (uint32, error) {
	if off < 0 || len(data)-off < 4 {
		return 0, ErrTruncated
	}
	return binary.BigEndian.Uint32(data[off : off+4]), nil
}

// U64 reads a big-endian uint64 at off, rejecting out-of-range access.
func U64(data []byte, off int) (uint64, error) {
	if off < 0 || len(data)-off < 8 {
		return 0, ErrTruncated
	}
	return binary.BigEndian.Uint64(data[off : off+8]), nil
}
