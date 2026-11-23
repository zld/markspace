// Package mp4 implements a streaming, fault-tolerant CMAF/fMP4 box and
// fragment reader. Parsing is incremental: every box records its absolute
// byte offset, and malformed input produces classified, locatable errors
// instead of panics.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"mediatool/internal/timeline"
)

// MaxFragmentSize is the hard upper bound for a single fragment (4 MiB,
// matching the performance contract). Larger declared box sizes are
// rejected with timeline.ClassBoxLength.
const MaxFragmentSize uint64 = 4 << 20 // 4 MiB

// BoxType is the four-character box type code.
type BoxType [4]byte

// String returns the ASCII box type, replacing non-printable bytes.
func (t BoxType) String() string {
	b := make([]byte, 4)
	for i, c := range t {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		} else {
			b[i] = c
		}
	}
	return string(b)
}

// BoxHeader describes one parsed box without retaining its payload.
type BoxHeader struct {
	Type          BoxType
	Offset        uint64 // absolute offset of the box header
	Size          uint64 // total size including the header
	HeaderSize    uint64 // 8 bytes, or 16 for 64-bit largesize
	PayloadOffset uint64 // absolute offset where the payload starts
	PayloadSize   uint64
	// ExtendToEOF is true when the declared size is zero.
	ExtendToEOF bool
}

// VisitFunc receives a box header and a limited reader over exactly its
// payload bytes. The reader must be fully consumed or skipped; ScanBoxes
// drains any remainder before reading the next header.
type VisitFunc func(box BoxHeader, payload io.Reader) error

// ScanBoxes incrementally parses boxes from r. base is the absolute offset
// of r's first byte within the containing stream (use 0 for top level).
// Parsing stops at the first malformed header; already visited boxes are
// not lost because visit runs inline. maxSize bounds every declared box.
func ScanBoxes(r io.Reader, base int64, maxSize uint64, visit VisitFunc) error {
	pos := uint64(base)
	if base < 0 {
		return timeline.ErrorAt(timeline.ClassOverflow, base, "negative box base offset")
	}
	var hdr [8]byte
	for {
		n, err := io.ReadFull(r, hdr[:])
		if errors.Is(err, io.EOF) {
			return nil // clean end of stream
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return timeline.ErrorAt(timeline.ClassBoxTruncated, int64(pos),
				fmt.Sprintf("box header truncated after %d of 8 bytes", n))
		}
		if err != nil {
			return err
		}

		declared := binary.BigEndian.Uint32(hdr[0:4])
		bt := BoxType{hdr[4], hdr[5], hdr[6], hdr[7]}
		box := BoxHeader{Type: bt, Offset: pos, HeaderSize: 8}

		switch declared {
		case 0:
			// size 0: box extends to end of stream.
			box.ExtendToEOF = true
		case 1:
			var ext [8]byte
			if _, err := io.ReadFull(r, ext[:]); err != nil {
				return timeline.ErrorAt(timeline.ClassBoxTruncated, int64(pos+8),
					"64-bit largesize header truncated")
			}
			box.HeaderSize = 16
			box.Size = binary.BigEndian.Uint64(ext[:])
			if box.Size < 16 {
				return timeline.ErrorAt(timeline.ClassBoxLength, int64(pos),
					fmt.Sprintf("box %s largesize %d smaller than 16-byte header", bt, box.Size))
			}
		default:
			box.Size = uint64(declared)
			if box.Size < 8 {
				return timeline.ErrorAt(timeline.ClassBoxLength, int64(pos),
					fmt.Sprintf("box %s size %d smaller than 8-byte header", bt, box.Size))
			}
		}

		if !box.ExtendToEOF {
			if box.Size > maxSize {
				return timeline.ErrorAt(timeline.ClassBoxLength, int64(pos),
					fmt.Sprintf("box %s size %d exceeds limit %d", bt, box.Size, maxSize))
			}
			box.PayloadSize = box.Size - box.HeaderSize
		}
		box.PayloadOffset = pos + box.HeaderSize

		payloadReader := io.LimitReader(r, int64(box.PayloadSize))
		if box.ExtendToEOF {
			payloadReader = r
		}
		if err := visit(box, payloadReader); err != nil {
			return err
		}
		// Drain any payload the visitor did not consume. A LimitedReader
		// with N still positive afterwards means the declared size ran
		// past end of input (truncated box).
		if !box.ExtendToEOF {
			lr := payloadReader.(*io.LimitedReader)
			if _, derr := io.Copy(io.Discard, lr); derr != nil && !errors.Is(derr, io.EOF) {
				return derr
			}
			if lr.N > 0 {
				return timeline.ErrorAt(timeline.ClassBoxTruncated, int64(box.PayloadOffset),
					fmt.Sprintf("box %s payload truncated: %d of %d bytes missing",
						bt, lr.N, box.PayloadSize))
			}
			pos += box.Size
		} else {
			return nil
		}
	}
}

// ReadFullBoxHeader parses the version/flags prefix of a FullBox. It must
// be called with a reader positioned at the first payload byte.
func ReadFullBoxHeader(r io.Reader) (version uint8, flags uint32, err error) {
	var buf [4]byte
	if _, err = io.ReadFull(r, buf[:]); err != nil {
		return 0, 0, timeline.NewError(timeline.ClassBoxTruncated,
			"full-box version/flags truncated")
	}
	return buf[0], binary.BigEndian.Uint32(buf[:]) & 0x00ffffff, nil
}

// BoxList collects box headers for callers that need random access.
type BoxList []BoxHeader

// First returns the first box of the given type, or false.
func (l BoxList) First(t BoxType) (BoxHeader, bool) {
	for _, b := range l {
		if b.Type == t {
			return b, true
		}
	}
	return BoxHeader{}, false
}

// CollectBoxes scans r and returns every box header in stream order.
func CollectBoxes(r io.Reader, base int64, maxSize uint64) (BoxList, error) {
	var list BoxList
	err := ScanBoxes(r, base, maxSize, func(box BoxHeader, payload io.Reader) error {
		list = append(list, box)
		return nil
	})
	return list, err
}
