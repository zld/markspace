// Package ebml implements a streaming, single-pass reader for the subset of
// EBML / Matroska / WebM elements produced by the medtrace fixtures. It
// supports both known-length and unknown-length elements; an unknown-length
// element terminates only at its enclosing parent boundary or at end of file.
//
// Every malformed construct is reported as a *mederr.Error carrying a byte
// offset and the element path — malformed data is never silently skipped.
package ebml

import (
	"io"
	"math/bits"

	"medtrace/internal/mederr"
)

// ElementID is a raw EBML element identifier including its VINT marker bits.
// Marker-bearing four-byte ids (e.g. Segment 0x18538067) need the full width.
type ElementID uint64

// Matroska / WebM element identifiers used by medtrace.
const (
	IDEBML            ElementID = 0x1A45DFA3
	IDEBMLVersion     ElementID = 0x4288
	IDEBMLReadVersion ElementID = 0x42F7
	IDEBMLMaxIDLength ElementID = 0x42F2
	IDEBMLMaxSizeLen  ElementID = 0x42F3
	IDDocType         ElementID = 0x4282
	IDDocTypeVersion  ElementID = 0x4287
	IDDocTypeReadVer  ElementID = 0x4285
	IDSegment         ElementID = 0x18538067
	IDInfo            ElementID = 0x1549A966
	IDTimecodeScale   ElementID = 0x2AD7B1
	IDMuxingApp       ElementID = 0x4D80
	IDWritingApp      ElementID = 0x5741
	IDTracks          ElementID = 0x1654AE6B
	IDTrackEntry      ElementID = 0xAE
	IDTrackNumber     ElementID = 0xD7
	IDTrackUID        ElementID = 0x73C5
	IDTrackType       ElementID = 0x83
	IDCodecID         ElementID = 0x86
	IDName            ElementID = 0x536E
	IDCluster         ElementID = 0x1F43B675
	IDTimecode        ElementID = 0xE7
	IDSimpleBlock     ElementID = 0xA3
	IDBlockGroup      ElementID = 0xA0
	IDBlock           ElementID = 0xA1
	IDVoid            ElementID = 0xEC
	IDCRC32           ElementID = 0xBF
)

// Element describes one EBML element header located in the input stream.
type Element struct {
	ID           ElementID
	HeaderOffset int64
	BodyOffset   int64
	Size         int64 // -1 when the element uses an unknown length
	UnknownSize  bool
	Path         []string // outer-first symbolic path
}

// End returns the exclusive end offset, or -1 for an unknown-length element.
func (e *Element) End() int64 {
	if e.UnknownSize {
		return -1
	}
	return e.BodyOffset + e.Size
}

// ReadSeeker is the stream contract required by Reader.
type ReadSeeker interface {
	io.Reader
	io.Seeker
}

// Reader walks EBML streams sequentially with bounded memory.
type Reader struct {
	r ReadSeeker
}

// NewReader wraps an EBML byte stream.
func NewReader(r ReadSeeker) *Reader { return &Reader{r: r} }

// Offset reports the current absolute byte position.
func (r *Reader) Offset() int64 {
	off, err := r.r.Seek(0, io.SeekCurrent)
	if err != nil {
		panic(err) // offsets on local files never fail
	}
	return off
}

// SeekTo moves the stream to an absolute offset.
func (r *Reader) SeekTo(off int64) error {
	_, err := r.r.Seek(off, io.SeekStart)
	return err
}

func (r *Reader) readByteAt(off int64) (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(r.r, b[:]); err != nil {
		if err == io.EOF {
			return 0, io.EOF // clean boundary: no byte at all
		}
		if err == io.ErrUnexpectedEOF {
			return 0, mederr.At(mederr.CatTruncatedHeader, off, "unexpected end of stream while reading VINT")
		}
		return 0, mederr.At(mederr.CatIO, off, "read failed").Wrap(err)
	}
	return b[0], nil
}

// readVINT decodes one EBML variable-size integer at the current position.
// width is the number of consumed bytes; unknown is true for the all-ones
// reserved value used by unknown-length size fields.
func (r *Reader) readVINT(off int64) (val uint64, width int, unknown bool, err error) {
	first, err := r.readByteAt(off)
	if err != nil {
		return 0, 0, false, err
	}
	if first == 0 {
		return 0, 0, false, mederr.At(mederr.CatInvalidVINT, off, "VINT starts with 0x00 and has no length marker")
	}
	width = bits.LeadingZeros8(first) + 1
	dataBits := 7 * width
	val = uint64(first & (0xFF >> uint(width)))
	for i := 1; i < width; i++ {
		b, err := r.readByteAt(off + int64(i))
		if err != nil {
			if err == io.EOF {
				return 0, width, false, mederr.At(mederr.CatTruncatedHeader, off+int64(i),
					"VINT truncated after %d of %d bytes", i, width)
			}
			return 0, width, false, err
		}
		val = (val << 8) | uint64(b)
	}
	if val == (uint64(1)<<uint(dataBits))-1 {
		unknown = true
	}
	return val, width, unknown, nil
}

// readRawVINT decodes an element-ID VINT, retaining the length marker bits so
// the result equals the element's canonical on-wire identifier (e.g. Segment
// 0x18538067). Element IDs are always compared by their full wire value;
// only size VINTs strip the marker via readVINT.
func (r *Reader) readRawVINT(off int64) (raw uint64, width int, unknown bool, err error) {
	first, err := r.readByteAt(off)
	if err != nil {
		return 0, 0, false, err
	}
	if first == 0 {
		return 0, 0, false, mederr.At(mederr.CatInvalidVINT, off, "VINT starts with 0x00 and has no length marker")
	}
	width = bits.LeadingZeros8(first) + 1
	raw = uint64(first)
	for i := 1; i < width; i++ {
		b, err := r.readByteAt(off + int64(i))
		if err != nil {
			if err == io.EOF {
				return 0, width, false, mederr.At(mederr.CatTruncatedHeader, off+int64(i),
					"VINT truncated after %d of %d bytes", i, width)
			}
			return 0, width, false, err
		}
		raw = (raw << 8) | uint64(b)
	}
	// Reserved all-ones data portion (marker excluded).
	if raw&((uint64(1)<<uint(7*width))-1) == (uint64(1)<<uint(7*width))-1 {
		unknown = true
	}
	return raw, width, unknown, nil
}

// ReadElementHeader reads the next element header without entering its body.
// boundary is the exclusive parent end, or -1 when the parent is unknown.
func (r *Reader) ReadElementHeader(boundary int64) (*Element, error) {
	off := r.Offset()
	if boundary >= 0 && off >= boundary {
		return nil, io.EOF
	}
	idVal, idWidth, idUnknown, err := r.readRawVINT(off)
	if err != nil {
		return nil, err
	}
	if idUnknown {
		return nil, mederr.At(mederr.CatInvalidVINT, off, "element id uses reserved all-ones VINT")
	}
	sizeOff := off + int64(idWidth)
	size, sizeWidth, sizeUnknown, err := r.readVINT(sizeOff)
	if err != nil {
		return nil, err
	}
	e := &Element{
		ID:           ElementID(idVal),
		HeaderOffset: off,
		BodyOffset:   sizeOff + int64(sizeWidth),
		Size:         int64(size),
		UnknownSize:  sizeUnknown,
	}
	if sizeUnknown {
		e.Size = -1
		return e, nil
	}
	if boundary >= 0 && e.BodyOffset+e.Size > boundary {
		return nil, mederr.At(mederr.CatLengthOutOfBounds, off,
			"element %s ends at %d beyond parent boundary %d", e.ID.Name(), e.BodyOffset+e.Size, boundary)
	}
	return e, nil
}

func bytesToHex(v uint64) string {
	const h = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = h[v&0xF]
		v >>= 4
	}
	return string(out)
}
