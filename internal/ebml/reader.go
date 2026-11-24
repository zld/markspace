// Package ebml is a byte-offset-aware EBML reader for Matroska/WebM streams.
// Unlike struct-tag unmarshalers it preserves the absolute byte interval of
// every element and block, which the rest of the kernel needs both for
// lossless remuxing and for reporting exactly where malformed input fails.
// Unknown-length elements (all-ones VINT size) are first-class: they run to
// the end of the enclosing element or the file.
package ebml

import (
	"encoding/binary"
	"io"
	"math"

	"mediapack/internal/fault"
)

// VINT width marker bits: index by the number of leading zero bits + 1.
var widthMarker = [8]byte{0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0x02, 0x01}

// UnknownSize is the all-ones size VINT meaning "until end of parent/file".
const UnknownSize int64 = -1

// maxVIntBits masks the data bits of a VINT of the given width in bytes.
var sizeMask = [8]uint64{
	0x7f, 0x3fff, 0x1fffff, 0x0fffffff,
	0x07ffffffff, 0x03ffffffffff, 0x01ffffffffffff, 0x00ffffffffffffff,
}

// Element is one parsed EBML element header together with its byte interval.
type Element struct {
	ID        uint32
	Size      int64 // UnknownSize (-1) when the element has unknown length
	Start     int64 // absolute offset of the ID's first byte
	HeaderEnd int64 // absolute offset where the payload starts
	End       int64 // absolute offset just past the payload; -1 when unknown
}

// PayloadOffset is the first byte belonging to the element payload.
func (e *Element) PayloadOffset() int64 { return e.HeaderEnd }

// Reader walks an EBML stream inside a fixed byte window.
type Reader struct {
	r    io.ReadSeeker
	base int64 // absolute offset of the window start
	end  int64 // absolute offset just past the window, -1 for whole file
	// physicalEnd is the underlying stream's EOF. When end == physicalEnd a
	// declared overrun cannot be distinguished from a truncated tail until a
	// payload read actually fails, so header checks defer to ReadPayload.
	physicalEnd int64
	// bounded marks a window derived from a known-length parent element:
	// overruns are structural length-out-of-range faults, not truncation.
	bounded bool
}

// NewReader wraps an existing seekable stream. limit < 0 means the window
// extends to EOF; otherwise reads are constrained to [offset, offset+limit).
func NewReader(r io.ReadSeeker, offset, limit int64) (*Reader, error) {
	size, seekErr := r.Seek(0, io.SeekEnd)
	end := UnknownSize
	physical := UnknownSize
	if seekErr == nil {
		physical = size
		end = size // whole-file window: exact end enables End on every element
	}
	if limit >= 0 {
		end = offset + limit
	}
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return nil, fault.New(fault.KindIO, offset, "seek failed: %v", err)
	}
	return &Reader{r: r, base: offset, end: end,
		physicalEnd: physical, bounded: limit >= 0}, nil
}

// Offset returns the reader's current absolute byte position.
func (r *Reader) Offset() int64 {
	off, err := r.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1
	}
	return off
}

// SeekTo moves the underlying stream to an absolute offset.
func (r *Reader) SeekTo(off int64) error {
	if _, err := r.r.Seek(off, io.SeekStart); err != nil {
		return fault.New(fault.KindIO, off, "seek: %v", err)
	}
	return nil
}

// Remaining reports how many payload bytes are left inside the window, or -1
// when the window reaches EOF.
func (r *Reader) Remaining() int64 {
	if r.end < 0 {
		return UnknownSize
	}
	rem := r.end - r.Offset()
	if rem < 0 {
		return 0
	}
	return rem
}

// readByte returns one byte or a structured truncation fault at off.
func (r *Reader) readByte(off int64) (byte, error) {
	var buf [1]byte
	n, err := io.ReadFull(r.r, buf[:])
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return 0, fault.New(fault.KindTruncated, off,
			"expected 1 byte at end of input").AsRecoverable()
	}
	if err != nil {
		return 0, fault.New(fault.KindIO, off, "read: %v", err)
	}
	if n != 1 {
		return 0, fault.New(fault.KindTruncated, off, "short read").AsRecoverable()
	}
	return buf[0], nil
}

// ReadVInt decodes one EBML variable-length integer starting at off. It
// returns the value, the encoded width in bytes and the raw VINT bytes. A
// reserved all-ones value is reported through reservedAllOnes so callers can
// distinguish an unknown-size marker from ordinary data.
func (r *Reader) ReadVInt(off int64) (value uint64, width int, raw []byte, err error) {
	if _, err = r.r.Seek(off, io.SeekStart); err != nil {
		return 0, 0, nil, fault.New(fault.KindIO, off, "seek: %v", err)
	}
	first, err := r.readByte(off)
	if err != nil {
		return 0, 0, nil, err
	}
	width = 1
	for width <= 8 && first&widthMarker[width-1] == 0 {
		width++
	}
	if width > 8 {
		return 0, 0, nil, fault.New(fault.KindIllegalVINT, off,
			"VINT length marker missing in first byte 0x%02x", first)
	}
	raw = make([]byte, width)
	raw[0] = first
	for i := 1; i < width; i++ {
		// The initial Seek above positioned the stream at off, so these
		// reads advance byte-by-byte in order.
		if raw[i], err = r.readByte(off + int64(i)); err != nil {
			return 0, 0, nil, err
		}
	}
	var v uint64
	for _, b := range raw {
		v = (v << 8) | uint64(b)
	}
	return v, width, raw, nil
}

// ReadElement parses the next element header at the current position. It
// validates the declared size against the enclosing window and rejects
// truncated headers without ever skipping bytes silently.
func (r *Reader) ReadElement() (*Element, error) {
	start := r.Offset()
	if r.end >= 0 && start >= r.end {
		return nil, io.EOF
	}
	idVal, idWidth, _, err := r.ReadVInt(start)
	if err != nil {
		return nil, err
	}
	el := &Element{Start: start, End: UnknownSize}
	// Element IDs are at most 4 bytes / 28 data bits in Matroska.
	if idWidth > 4 {
		return nil, fault.New(fault.KindIllegalVINT, start,
			"element ID width %d exceeds 4 bytes", idWidth)
	}
	// The reserved all-ones VINT (data bits all set, e.g. 0xFF) is never a
	// legal element ID.
	if idVal&sizeMask[idWidth-1] == sizeMask[idWidth-1] {
		return nil, fault.New(fault.KindIllegalVINT, start,
			"element ID is the reserved all-ones VINT 0x%x", idVal)
	}
	el.ID = uint32(idVal)
	sizeOff := r.Offset()
	sizeVal, sizeWidth, _, err := r.ReadVInt(sizeOff)
	if err != nil {
		return nil, err
	}
	el.HeaderEnd = sizeOff + int64(sizeWidth)
	el.End = UnknownSize
	if sizeVal&sizeMask[sizeWidth-1] == sizeMask[sizeWidth-1] {
		el.Size = UnknownSize // unknown-length element
	} else {
		el.Size = int64(sizeVal & sizeMask[sizeWidth-1])
		el.End = el.HeaderEnd + el.Size
		// A bounded parent makes overruns structural. An unknown-length
		// parent ending at physical EOF defers to the payload read so a
		// truncated tail is reported at the exact failing byte.
		if r.end >= 0 && el.End > r.end && (r.bounded || r.physicalEnd < 0 || r.end != r.physicalEnd) {
			return nil, fault.New(fault.KindLengthOutOfRange, sizeOff,
				"element size %d ending at %d exceeds parent end %d",
				el.Size, el.End, r.end)
		}
	}
	return el, nil
}

// Child returns a reader constrained to this element's payload window. An
// unknown-length child inherits the parent's remaining window.
func (r *Reader) Child(el *Element) (*Reader, error) {
	if _, err := r.r.Seek(el.HeaderEnd, io.SeekStart); err != nil {
		return nil, fault.New(fault.KindIO, el.HeaderEnd, "seek: %v", err)
	}
	child := &Reader{r: r.r, base: el.HeaderEnd, physicalEnd: r.physicalEnd}
	if el.Size >= 0 {
		// Known-length element: an explicit structural window, regardless
		// of whether it coincides with physical EOF.
		child.end = el.HeaderEnd + el.Size
		child.bounded = true
	} else if r.end < 0 {
		child.end = UnknownSize
		child.bounded = r.bounded
	} else {
		// Unknown-length child inherits the parent's implicit window.
		child.end = r.end
		child.bounded = r.bounded
	}
	return child, nil
}

// Skip moves the stream past el's payload. Unknown-length elements cannot be
// skipped blindly: the caller must Drain them to a known parent boundary.
func (r *Reader) Skip(el *Element) error {
	target := el.End
	if target < 0 {
		return fault.New(fault.KindLengthOutOfRange, el.Start,
			"cannot skip unknown-length element %x", el.ID)
	}
	if _, err := r.r.Seek(target, io.SeekStart); err != nil {
		return fault.New(fault.KindIO, target, "seek: %v", err)
	}
	return nil
}

// Drain consumes an unknown-length master element by walking its children
// until the current window ends. Every child is fully traversed, so bytes are
// never silently discarded. A truncated tail surfaces as a recoverable fault.
func (r *Reader) Drain() error {
	for {
		el, err := r.ReadElement()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if el.Size < 0 {
			child, err := r.Child(el)
			if err != nil {
				return err
			}
			if err := child.Drain(); err != nil {
				return err
			}
			continue
		}
		if err := r.Skip(el); err != nil {
			return err
		}
	}
}

// ReadPayload copies the full element payload into a freshly allocated slice,
// honoring the window boundary. Used for small master children (headers).
func (r *Reader) ReadPayload(el *Element) ([]byte, error) {
	if el.Size < 0 {
		return nil, fault.New(fault.KindLengthOutOfRange, el.Start,
			"element %x has unknown length, stream instead", el.ID)
	}
	buf := make([]byte, el.Size)
	if _, err := r.r.Seek(el.HeaderEnd, io.SeekStart); err != nil {
		return nil, fault.New(fault.KindIO, el.HeaderEnd, "seek: %v", err)
	}
	n, err := io.ReadFull(r.r, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return nil, fault.New(fault.KindTruncated, el.HeaderEnd+int64(n),
			"element %x payload truncated: got %d of %d bytes", el.ID, n, el.Size).
			AsRecoverable()
	}
	if err != nil {
		return nil, fault.New(fault.KindIO, el.HeaderEnd, "read: %v", err)
	}
	return buf, nil
}

// ReadUint decodes a big-endian unsigned integer payload (up to 8 bytes).
func ReadUint(b []byte) (uint64, error) {
	if len(b) > 8 {
		return 0, fault.New(fault.KindLengthOutOfRange, -1,
			"uint payload %d bytes exceeds 8", len(b))
	}
	var v uint64
	for _, c := range b {
		v = (v << 8) | uint64(c)
	}
	return v, nil
}

// ReadFloat decodes a 0/4/8-byte EBML float payload.
func ReadFloat(b []byte) (float64, error) {
	switch len(b) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	default:
		return 0, fault.New(fault.KindLengthOutOfRange, -1,
			"float payload must be 0, 4 or 8 bytes, got %d", len(b))
	}
}
