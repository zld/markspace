// Package ebml parses EBML element structure (the container layer of
// Matroska/WebM). It distinguishes known-length from unknown-length
// elements; unknown-length elements are only accepted on master elements
// and terminate at their legal parent boundary. All malformed input
// (bad VINTs, out-of-bounds sizes, truncated headers or data) is
// reported with byte offset, element path and a stable error code.
package ebml

import (
	"encoding/binary"
	"math"

	"continuity/internal/cerr"
)

// UnknownSize marks an element whose size VINT is all ones.
const UnknownSize = int64(-1)

// Element IDs used by the parser and fixtures.
const (
	IDEBML                    = 0x1A45DFA3
	IDEBMLVersion             = 0x4286
	IDEBMLReadVersion         = 0x42F7
	IDEBMLMaxIDLength         = 0x42F2
	IDEBMLMaxSizeLength       = 0x42F3
	IDDocType                 = 0x4282
	IDDocTypeVersion          = 0x4287
	IDDocTypeReadVersion      = 0x4285
	IDSegment                 = 0x18538067
	IDInfo                    = 0x1549A966
	IDTimecodeScale           = 0x2AD7B1
	IDMuxingApp               = 0x4D80
	IDWritingApp              = 0x5741
	IDTracks                  = 0x1654AE6B
	IDTrackEntry              = 0xAE
	IDTrackNumber             = 0xD7
	IDTrackUID                = 0x73C5
	IDTrackType               = 0x83
	IDCodecID                 = 0x86
	IDVideo                   = 0xE0
	IDPixelWidth              = 0xB0
	IDPixelHeight             = 0xBA
	IDColour                  = 0x55B0
	IDMatrixCoefficients      = 0x55B1
	IDTransferCharacteristics = 0x55BA
	IDPrimaries               = 0x55BB
	IDRange                   = 0x55B9
	IDAudio                   = 0xE1
	IDSamplingFrequency       = 0xB5
	IDChannels                = 0x9F
	IDCluster                 = 0x1F43B675
	IDTimecode                = 0xE7
	IDSimpleBlock             = 0xA3
	IDBlockGroup              = 0xA0
	IDBlock                   = 0xA1
	IDBlockDuration           = 0x9B
)

var names = map[uint32]string{
	IDEBML: "EBML", IDEBMLVersion: "EBMLVersion", IDEBMLReadVersion: "EBMLReadVersion",
	IDEBMLMaxIDLength: "EBMLMaxIDLength", IDEBMLMaxSizeLength: "EBMLMaxSizeLength",
	IDDocType: "DocType", IDDocTypeVersion: "DocTypeVersion", IDDocTypeReadVersion: "DocTypeReadVersion",
	IDSegment: "Segment", IDInfo: "Info", IDTimecodeScale: "TimecodeScale",
	IDMuxingApp: "MuxingApp", IDWritingApp: "WritingApp",
	IDTracks: "Tracks", IDTrackEntry: "TrackEntry", IDTrackNumber: "TrackNumber",
	IDTrackUID: "TrackUID", IDTrackType: "TrackType", IDCodecID: "CodecID",
	IDVideo: "Video", IDPixelWidth: "PixelWidth", IDPixelHeight: "PixelHeight",
	IDColour: "Colour", IDMatrixCoefficients: "MatrixCoefficients",
	IDTransferCharacteristics: "TransferCharacteristics", IDPrimaries: "Primaries", IDRange: "Range",
	IDAudio: "Audio", IDSamplingFrequency: "SamplingFrequency", IDChannels: "Channels",
	IDCluster: "Cluster", IDTimecode: "Timecode", IDSimpleBlock: "SimpleBlock",
	IDBlockGroup: "BlockGroup", IDBlock: "Block", IDBlockDuration: "BlockDuration",
}

var masters = map[uint32]bool{
	IDEBML: true, IDSegment: true, IDInfo: true, IDTracks: true,
	IDTrackEntry: true, IDVideo: true, IDColour: true, IDAudio: true,
	IDCluster: true, IDBlockGroup: true,
}

// Name returns the human-readable name of an element ID.
func Name(id uint32) string {
	if n, ok := names[id]; ok {
		return n
	}
	return "Unknown"
}

// IsMaster reports whether the element ID is a known master element.
func IsMaster(id uint32) bool { return masters[id] }

// Element is a parsed element header.
type Element struct {
	ID         uint32
	Offset     int64 // absolute offset of the element ID
	DataOffset int64 // absolute offset of the element data
	Size       int64 // data size in bytes, UnknownSize when unknown
	Level      int
	Path       string // e.g. "/Segment/Cluster/SimpleBlock"
}

// Unknown reports whether the element has unknown size.
func (e Element) Unknown() bool { return e.Size == UnknownSize }

// Reader parses EBML elements from an in-memory buffer.
type Reader struct {
	Data []byte
}

// New returns a Reader over data.
func New(data []byte) *Reader { return &Reader{Data: data} }

// WalkFunc is invoked for every element in document order. data holds
// the element payload for known-size elements and is nil for
// unknown-size elements. Returning descend=true walks the children of a
// master element. Unknown-size master elements are always descended.
type WalkFunc func(el Element, data []byte) (descend bool, err error)

// Walk parses the whole buffer.
func (r *Reader) Walk(fn WalkFunc) error {
	return r.walk(0, int64(len(r.Data)), "", 0, fn)
}

func (r *Reader) walk(start, end int64, parentPath string, level int, fn WalkFunc) error {
	pos := start
	for pos < end {
		el, err := r.readElement(pos, end, parentPath, level)
		if err != nil {
			return err
		}
		if el.Unknown() {
			if !IsMaster(el.ID) {
				return cerr.New(cerr.CodeUnknownSizeBounds, el.Offset, el.Path,
					"unknown-size element is not a master element")
			}
			if _, err := fn(el, nil); err != nil {
				return err
			}
			// An unknown-size master terminates only at its legal
			// parent boundary: its children occupy the rest of the
			// parent range.
			return r.walk(el.DataOffset, end, el.Path, level+1, fn)
		}
		dataEnd := el.DataOffset + el.Size
		descend, err := fn(el, r.Data[el.DataOffset:dataEnd])
		if err != nil {
			return err
		}
		if descend && IsMaster(el.ID) {
			if err := r.walk(el.DataOffset, dataEnd, el.Path, level+1, fn); err != nil {
				return err
			}
		}
		pos = dataEnd
	}
	return nil
}

func (r *Reader) readElement(pos, end int64, parentPath string, level int) (Element, error) {
	el := Element{Offset: pos, Level: level, Path: parentPath}
	id, idLen, err := vintAt(r.Data, pos, end, true, parentPath)
	if err != nil {
		return el, err
	}
	if idLen > 4 {
		return el, cerr.New(cerr.CodeVINTInvalid, pos, parentPath, "element ID longer than 4 bytes")
	}
	el.ID = uint32(id)
	el.Path = parentPath + "/" + Name(el.ID)
	sz, szLen, err := vintAt(r.Data, pos+int64(idLen), end, false, el.Path)
	if err != nil {
		return el, err
	}
	el.DataOffset = pos + int64(idLen+szLen)
	if sz == unknownMarker(szLen) {
		el.Size = UnknownSize
		return el, nil
	}
	if sz > math.MaxInt64 {
		return el, cerr.New(cerr.CodeSizeOverflow, el.Offset, el.Path, "element size overflows int64")
	}
	el.Size = int64(sz)
	if el.DataOffset+el.Size > end {
		if end == int64(len(r.Data)) {
			return el, cerr.New(cerr.CodeTruncatedData, el.Offset, el.Path, "element data truncated")
		}
		return el, cerr.New(cerr.CodeSizeOverflow, el.Offset, el.Path, "element size exceeds parent boundary")
	}
	return el, nil
}

func unknownMarker(length int) uint64 {
	return (uint64(1) << (7 * uint(length))) - 1
}

// vintAt parses a variable-length integer at pos. With keepMarker the
// length marker bit is retained (element IDs); otherwise it is stripped
// (sizes, track numbers).
func vintAt(data []byte, pos, end int64, keepMarker bool, path string) (uint64, int, error) {
	if pos >= end {
		return 0, 0, cerr.New(cerr.CodeTruncatedHeader, pos, path, "truncated VINT")
	}
	first := data[pos]
	length := 1
	mask := byte(0x80)
	for length <= 8 && first&mask == 0 {
		length++
		mask >>= 1
	}
	if length > 8 {
		return 0, 0, cerr.New(cerr.CodeVINTInvalid, pos, path, "invalid VINT length marker")
	}
	if pos+int64(length) > end {
		return 0, 0, cerr.New(cerr.CodeTruncatedHeader, pos, path, "truncated VINT")
	}
	v := uint64(first)
	if !keepMarker {
		v &^= uint64(mask)
	}
	for i := 1; i < length; i++ {
		v = v<<8 | uint64(data[pos+int64(i)])
	}
	return v, length, nil
}

// VINTValue parses a marker-stripped VINT from the head of data,
// returning the value and the number of bytes consumed.
func VINTValue(data []byte) (uint64, int, error) {
	return vintAt(data, 0, int64(len(data)), false, "vint")
}

// Uint decodes a big-endian unsigned integer element payload.
func Uint(data []byte) uint64 {
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v
}

// Float decodes a 4- or 8-byte IEEE float element payload.
func Float(data []byte) float64 {
	switch len(data) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(data)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(data))
	}
	return 0
}
