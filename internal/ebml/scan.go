// Package ebml performs bounded, streaming scans of Matroska/WebM containers.
// Only element headers are read into memory; block payloads stay on disk and
// are reached through the offsets recorded in the index.
package ebml

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// Matroska element identifiers used by the scanner. These are the raw VINT
// encodings as they appear on the wire (length marker included), matching
// Element.ID directly.
const (
	IDEBML            = 0x1A45DFA3
	IDEBMLDocType     = 0x4282
	IDSegment         = 0x18538067
	IDInfo            = 0x1549A966
	IDTimecodeScale   = 0x2AD7B1
	IDMuxingApp       = 0x4D80
	IDWritingApp      = 0x5741
	IDTracks          = 0x1654AE6B
	IDTrackEntry      = 0xAE
	IDTrackNumber     = 0xD7
	IDTrackType       = 0x83
	IDCodecID         = 0x86
	IDAudio           = 0xE1
	IDSamplingFreq    = 0xB5
	IDChannels        = 0x9F
	IDCluster         = 0x1F43B675
	IDTimecode        = 0xE7
	IDSimpleBlock     = 0xA3
	IDBlockGroup      = 0xA0
	IDBlock           = 0xA1
	IDReferenceBlock  = 0xFB
)

// ErrorCode identifies a stable failure class independent of wording.
type ErrorCode string

const (
	ErrTruncatedVINT ErrorCode = "EBML_TRUNCATED_VINT"
	ErrInvalidVINT   ErrorCode = "EBML_INVALID_VINT"
	ErrTruncatedElem ErrorCode = "EBML_TRUNCATED_ELEMENT"
	ErrInvalidEBML   ErrorCode = "EBML_INVALID_CONTAINER"
	ErrLimitExceeded ErrorCode = "EBML_LIMIT_EXCEEDED"
)

// Error carries a stable code and the offset of the first offending byte.
type Error struct {
	Code   ErrorCode
	Offset int64
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s at byte %d: %s", e.Code, e.Offset, e.Msg)
}

// Limits bounds scan work so hostile inputs cannot exhaust memory or time.
type Limits struct {
	MaxFileBytes int64
	MaxTracks    int
	MaxBlocks    int
}

// DefaultLimits are the processing ceilings documented for the tool.
var DefaultLimits = Limits{MaxFileBytes: 512 << 20, MaxTracks: 16, MaxBlocks: 1_000_000}

// Element is one parsed EBML element header. Payload is never loaded.
type Element struct {
	ID        uint64 // spec VINT ID, length marker included (e.g. 0x1F43B675)
	DataValue uint64 // marker-stripped ID value
	Offset    int64  // offset of the ID's first byte
	Size      int64  // payload size; -1 means unknown length
	DataOff   int64  // payload start
	End       int64  // payload end; -1 when unknown length
}

// UnknownSize marks an element whose size VINT has all data bits set.
const UnknownSize = int64(-1)

// ReadVINT reads one EBML VINT (length-prefixed integer). The returned value
// keeps the length marker bit set, which is exactly how element IDs are
// encoded; size callers strip it with VINTData. off is the offset of the
// first byte read; errors report that byte as the failure offset.
func ReadVINT(r io.Reader, off int64) (value uint64, width int, unknown bool, err error) {
	var first [1]byte
	if _, err = io.ReadFull(r, first[:]); err != nil {
		return 0, 0, false, &Error{ErrTruncatedVINT, off, "missing VINT lead byte"}
	}
	lead := first[0]
	if lead == 0 {
		return 0, 0, false, &Error{ErrInvalidVINT, off, "zero VINT lead byte"}
	}
	// Width is the position of the lead byte's highest set bit: 0x80->1,
	// 0x40->2, ... 0x01->8.
	width = 1
	for lead&(0x80>>uint(width-1)) == 0 {
		width++
	}
	if width > 8 {
		return 0, 0, false, &Error{ErrInvalidVINT, off, "VINT width > 8"}
	}
	rest := make([]byte, width-1)
	if _, err = io.ReadFull(r, rest); err != nil {
		return 0, 0, false, &Error{ErrTruncatedVINT, off, "VINT payload truncated"}
	}
	mask := byte(0xFF >> uint(width)) // data bits of the lead byte
	value = uint64(lead)
	// Unknown length: every data bit is 1. Check the lead data bits and each
	// full trailing byte independently of the marker.
	allOnes := lead&mask == mask && mask != 0
	for _, b := range rest {
		value = value<<8 | uint64(b)
		if b != 0xFF {
			allOnes = false
		}
	}
	return value, width, allOnes, nil
}

// VINTData strips the length marker from an encoded VINT value.
func VINTData(value uint64, width int) uint64 {
	return value &^ (uint64(0x80) << uint(7*(width-1)))
}

// ReadElementHeader parses an element header at the current reader position.
// The all-ones VINT form is legal as an element ID; only a size VINT uses it
// to mean unknown length.
func ReadElementHeader(r io.Reader, off int64) (Element, error) {
	id, idw, _, err := ReadVINT(r, off)
	if err != nil {
		return Element{}, err
	}
	sizeOff := off + int64(idw)
	sizeRaw, sw, sizeUnknown, err := ReadVINT(r, sizeOff)
	if err != nil {
		return Element{}, err
	}
	if idw > 4 {
		return Element{}, &Error{ErrInvalidVINT, off, "element ID wider than 4 bytes"}
	}
	size := VINTData(sizeRaw, sw)
	e := Element{ID: id, DataValue: VINTData(id, idw), Offset: off, DataOff: sizeOff + int64(sw)}
	if sizeUnknown {
		e.Size, e.End = UnknownSize, UnknownSize
	} else {
		if size > math.MaxInt64-uint64(e.DataOff) {
			return Element{}, &Error{ErrInvalidEBML, sizeOff, "element size overflows int64"}
		}
		e.Size = int64(size)
		e.End = e.DataOff + e.Size
	}
	return e, nil
}

func readFullAt(r io.ReaderAt, off, n int64) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(io.NewSectionReader(r, off, n), buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ReadUint parses an EBML unsigned integer payload.
func ReadUint(r io.ReaderAt, e Element) (uint64, error) {
	if e.Size < 0 || e.Size > 8 {
		return 0, &Error{ErrInvalidEBML, e.Offset, "bad uint width"}
	}
	buf, err := readFullAt(r, e.DataOff, e.Size)
	if err != nil {
		return 0, &Error{ErrTruncatedElem, e.DataOff, err.Error()}
	}
	var v uint64
	for _, b := range buf {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// ReadFloat parses an EBML float payload (0, 4 or 8 bytes).
func ReadFloat(r io.ReaderAt, e Element) (float64, error) {
	buf, err := readFullAt(r, e.DataOff, e.Size)
	if err != nil {
		return 0, &Error{ErrTruncatedElem, e.DataOff, err.Error()}
	}
	switch e.Size {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(beUint32(buf))), nil
	case 8:
		return math.Float64frombits(beUint64(buf)), nil
	default:
		return 0, &Error{ErrInvalidEBML, e.Offset, "bad float width"}
	}
}

// ReadString parses an EBML ASCII/UTF-8 string payload.
func ReadString(r io.ReaderAt, e Element) (string, error) {
	if e.Size < 0 {
		return "", &Error{ErrInvalidEBML, e.Offset, "unknown-length string"}
	}
	buf, err := readFullAt(r, e.DataOff, e.Size)
	if err != nil {
		return "", &Error{ErrTruncatedElem, e.DataOff, err.Error()}
	}
	return string(buf), nil
}

func beUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func beUint64(b []byte) uint64 {
	v := uint64(0)
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// Next returns the offset just past the element. bound is the parent end used
// when the element has unknown length.
func (e Element) Next(bound int64) (int64, error) {
	if e.End >= 0 {
		if e.End > bound {
			return 0, &Error{ErrInvalidEBML, e.Offset, "element overruns parent"}
		}
		return e.End, nil
	}
	return bound, nil
}

// walkChildren invokes fn for every child element of parent. Unknown-length
// children are rejected: only Segment/Cluster tolerate that, and both are
// handled explicitly by their callers.
func walkChildren(r io.ReaderAt, parent Element, fn func(Element) error) error {
	if parent.End < 0 {
		return &Error{ErrInvalidEBML, parent.Offset, "unknown-length element cannot be walked this way"}
	}
	return walkChildrenAt(r, parent.DataOff, parent.End, fn)
}

func walkChildrenAt(r io.ReaderAt, start, end int64, fn func(Element) error) error {
	pos := start
	for pos < end {
		el, err := ReadElementHeader(io.NewSectionReader(r, pos, end-pos), pos)
		if err != nil {
			return err
		}
		next, err := el.Next(end)
		if err != nil {
			return err
		}
		if err := fn(el); err != nil {
			return err
		}
		pos = next
	}
	if pos != end {
		return &Error{ErrInvalidEBML, end, "child elements overrun parent"}
	}
	return nil
}

// AsError unwraps an *Error from any error chain.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
