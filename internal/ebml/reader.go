package ebml

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"

	"mediacheck/internal/report"
)

// EBML and Matroska element IDs used by the local validator and sample
// generator. The parser also preserves IDs not listed here as raw uint32s.
const (
	IDEBMLHeader    = 0x1A45DFA3
	IDVersion       = 0x4286
	IDReadVersion   = 0x42F7
	IDMaxIDWidth    = 0x42F2
	IDMaxSizeWidth  = 0x42F3
	IDDocType       = 0x4282
	IDSegment       = 0x18538067
	IDSeekHead      = 0x114D9B74
	IDInfo          = 0x1549A966
	IDTimecodeScale = 0x2AD7B1
	IDMuxingApp     = 0x4D80
	IDWritingApp    = 0x5741

	IDTrackEntry      = 0xAE
	IDTracks          = 0x1654AE6B
	IDTrackNumber     = 0xD7
	IDTrackUID        = 0x73C5
	IDTrackType       = 0x83
	IDCodecID         = 0x86
	IDTrackDependsOn  = 0x53F8
	IDTrackDefaultDur = 0x23E383
	IDAudio           = 0xE1
	IDChannels        = 0x9F
	IDChannelLayout   = 0x55D0
	IDSamplingFreq    = 0xB5
	IDOutputSampling  = 0x78B5
	IDBitDepth        = 0x6264
	IDVideo           = 0xE0
	IDPixelWidth      = 0xB0
	IDPixelHeight     = 0xBA
	IDColour          = 0x55B0
	IDMatrixCoeffs    = 0x55B1
	IDTransferChars   = 0x55BA
	IDColourPrimaries = 0x55BB

	IDCluster     = 0x1F43B675
	IDTimecode    = 0xE7
	IDSimpleBlock = 0xA3
	IDBlockGroup  = 0xA0
	IDBlock       = 0xA1
	IDBlockDur    = 0x9B
	IDCues        = 0x1C53BB6B
	IDTags        = 0x1254C367
	IDChapters    = 0x1043A770
	IDAttachments = 0x1941A469
)

// Track types from the Matroska specification.
const (
	TrackTypeVideo    = 1
	TrackTypeAudio    = 2
	TrackTypeSubtitle = 17
)

// Limits bounds memory and parser work. The zero value is not usable; callers
// should start with DefaultLimits and override fields deliberately.
type Limits struct {
	MaxVINTWidth    int
	MaxIDWidth      int
	MaxElementSize  int64
	MaxBlocks       int
	MaxFragmentSpan int64
}

// DefaultLimits returns conservative local processing bounds.
func DefaultLimits() Limits {
	return Limits{
		MaxVINTWidth:    8,
		MaxIDWidth:      4,
		MaxElementSize:  64 << 20, // 64 MiB
		MaxBlocks:       1_000_000,
		MaxFragmentSpan: 300_000_000_000, // 5 minutes in nanoseconds
	}
}

// VINT is a decoded EBML variable-length integer.
type VINT struct {
	Offset  int
	Width   int
	Raw     uint64 // all VINT bytes, including the marker bit
	Value   uint64 // payload/size value with marker removed
	Unknown bool
}

// Element is a located EBML element. Offset is the first ID byte and
// PayloadOffset is the first byte after ID and size.
type Element struct {
	ID            uint32
	Offset        int
	IDWidth       int
	SizeOffset    int
	SizeWidth     int
	HeaderSize    int
	PayloadOffset int
	Size          int64 // -1 when unknown
	Known         bool
	End           int // exclusive payload end; for unknown elements set by boundary parsing
	Children      []*Element
}

// HexID renders an element ID as a stable eight-digit hex string.
func HexID(id uint32) string {
	return fmt.Sprintf("0x%08X", id)
}

// Reader performs offset-preserving low-level EBML reads over one byte slice.
type Reader struct {
	data   []byte
	limits Limits
}

// NewReader constructs an EBML reader.
func NewReader(data []byte, limits Limits) *Reader {
	if limits.MaxVINTWidth <= 0 {
		limits = DefaultLimits()
	}
	return &Reader{data: data, limits: limits}
}

// Data returns the underlying input bytes.
func (r *Reader) Data() []byte { return r.data }

// VINTAt decodes a VINT at offset using at most maxWidth bytes.
func (r *Reader) VINTAt(offset, maxWidth int) (VINT, *report.Error) {
	v := VINT{Offset: offset}
	if offset < 0 || offset >= len(r.data) {
		return v, report.NewError(report.EBMLTruncated, "truncated_vint", "", offset, len(r.data),
			"VINT starts beyond end of input")
	}
	first := r.data[offset]
	if first == 0 {
		return v, report.NewError(report.EBMLInvalidVINT, "invalid_vint", "", offset, offset,
			"VINT first byte is zero")
	}
	width := bits.LeadingZeros8(first) + 1
	if width > maxWidth || width > r.limits.MaxVINTWidth {
		return v, report.NewError(report.EBMLIllegalLength, "illegal_vint_width", "", offset, offset,
			"VINT width %d exceeds maximum %d", width, maxWidth)
	}
	v.Width = width
	if offset+width > len(r.data) {
		return v, report.NewError(report.EBMLTruncated, "truncated_vint", "", offset, len(r.data),
			"need %d VINT bytes, only %d remain", width, len(r.data)-offset)
	}
	var raw uint64
	for i := 0; i < width; i++ {
		raw = raw<<8 | uint64(r.data[offset+i])
	}
	v.Raw = raw
	dataBits := 7 * width
	valueMask := uint64((1 << dataBits) - 1)
	v.Value = raw & valueMask
	v.Unknown = v.Value == valueMask
	return v, nil
}

// ReadUint decodes an unsigned EBML integer payload.
func (r *Reader) ReadUint(e *Element) (uint64, *report.Error) {
	if !e.Known || e.Size < 0 || e.Size > 8 {
		return 0, report.NewError(report.EBMLIllegalLength, "invalid_uint_length", HexID(e.ID), e.Offset, e.PayloadOffset,
			"unsigned integer length %d is outside 0..8", e.Size)
	}
	if e.End > len(r.data) || e.PayloadOffset+int(e.Size) > len(r.data) {
		return 0, report.NewError(report.EBMLTruncated, "truncated_uint", HexID(e.ID), e.Offset, len(r.data),
			"unsigned integer payload is truncated")
	}
	buf := make([]byte, 8)
	copy(buf[8-e.Size:], r.data[e.PayloadOffset:e.PayloadOffset+int(e.Size)])
	return binary.BigEndian.Uint64(buf), nil
}

// ReadFloat decodes a 4- or 8-byte big-endian EBML float.
func (r *Reader) ReadFloat(e *Element) (float64, *report.Error) {
	if !e.Known || (e.Size != 4 && e.Size != 8) {
		return 0, report.NewError(report.EBMLIllegalLength, "invalid_float_length", HexID(e.ID), e.Offset, e.PayloadOffset,
			"float length %d must be 4 or 8", e.Size)
	}
	if e.PayloadOffset+int(e.Size) > len(r.data) {
		return 0, report.NewError(report.EBMLTruncated, "truncated_float", HexID(e.ID), e.Offset, len(r.data),
			"float payload is truncated")
	}
	b := r.data[e.PayloadOffset : e.PayloadOffset+int(e.Size)]
	if e.Size == 4 {
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
}

// ReadString decodes an EBML UTF-8/ASCII string payload.
func (r *Reader) ReadString(e *Element) (string, *report.Error) {
	if !e.Known || e.Size < 0 || e.PayloadOffset+int(e.Size) > len(r.data) {
		return "", report.NewError(report.EBMLTruncated, "truncated_string", HexID(e.ID), e.Offset, len(r.data),
			"string payload is truncated")
	}
	return string(r.data[e.PayloadOffset:e.End]), nil
}

// ReadElementAt decodes an element header and checks its declared bounds
// against parentEnd (exclusive). parentEnd may be len(data) for the root.
func (r *Reader) ReadElementAt(offset, parentEnd int) (*Element, *report.Error) {
	if parentEnd <= 0 || parentEnd > len(r.data) {
		parentEnd = len(r.data)
	}
	idV, err := r.VINTAt(offset, r.limits.MaxIDWidth)
	if err != nil {
		return nil, err
	}
	e := &Element{
		ID:         uint32(idV.Raw),
		Offset:     offset,
		IDWidth:    idV.Width,
		SizeOffset: offset + idV.Width,
	}
	sizeV, err := r.VINTAt(e.SizeOffset, r.limits.MaxVINTWidth)
	if err != nil {
		err.Record.ElementID = HexID(e.ID)
		if err.Record.InputOffset == 0 {
			err.Record.InputOffset = offset
		}
		return nil, err
	}
	e.SizeWidth = sizeV.Width
	e.HeaderSize = idV.Width + sizeV.Width
	e.PayloadOffset = e.SizeOffset + sizeV.Width

	if sizeV.Unknown {
		e.Size = -1
		e.Known = false
		return e, nil
	}
	e.Known = true
	e.Size = int64(sizeV.Value)
	if e.Size > r.limits.MaxElementSize {
		return nil, report.NewError(report.EBMLLimit, "element_too_large", HexID(e.ID), e.Offset, e.SizeOffset,
			"element size %d exceeds limit %d", e.Size, r.limits.MaxElementSize)
	}
	end64 := int64(e.PayloadOffset) + e.Size
	if end64 > int64(len(r.data)) {
		return nil, report.NewError(report.EBMLTruncated, "truncated_element", HexID(e.ID), e.Offset, len(r.data),
			"element declares %d payload bytes ending at %d, input ends at %d", e.Size, end64, len(r.data))
	}
	if end64 > int64(parentEnd) {
		return nil, report.NewError(report.EBMLIllegalLength, "element_exceeds_parent", HexID(e.ID), e.Offset, parentEnd,
			"element ends at %d beyond parent boundary %d", end64, parentEnd)
	}
	e.End = int(end64)
	return e, nil
}
