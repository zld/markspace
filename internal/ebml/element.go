package ebml

import (
	"tracecheck/internal/serr"
)

// ElementID is a decoded EBML element identifier (the data bits of its VINT).
type ElementID uint64

// Matroska/WebM element IDs used by this project. The subset covers the EBML
// header, segment metadata, track descriptions, clusters and blocks.
const (
	IDEBMLHeader      ElementID = 0x1A45DFA3
	IDEBMLVersion     ElementID = 0x4286
	IDEBMLReadVersion ElementID = 0x42F7
	IDEBMLMaxIDLength ElementID = 0x42F2
	IDEBMLMaxSizeLen  ElementID = 0x42F3
	IDDocType         ElementID = 0x4282
	IDDocTypeVersion  ElementID = 0x4287
	IDDocTypeReadVer  ElementID = 0x4285

	IDSegment       ElementID = 0x18538067
	IDSeekHead      ElementID = 0x114D9B74
	IDInfo          ElementID = 0x1549A966
	IDTimecodeScale ElementID = 0x2AD7B1
	IDMuxingApp     ElementID = 0x4D80
	IDWritingApp    ElementID = 0x5741
	IDDuration      ElementID = 0x4489

	IDTracks      ElementID = 0x1654AE6B
	IDTrackEntry  ElementID = 0xAE
	IDTrackNumber ElementID = 0xD7
	IDTrackUID    ElementID = 0x73C5
	IDTrackType   ElementID = 0x83
	IDFlagDefault ElementID = 0x88
	IDName        ElementID = 0x536E
	IDLanguage    ElementID = 0x22B59C
	IDCodecID     ElementID = 0x86

	IDAudio              ElementID = 0xE1
	IDVideo              ElementID = 0xE0
	IDColour             ElementID = 0x55B0
	IDSamplingFrequency  ElementID = 0xB5
	IDChannels           ElementID = 0x9F
	IDChannelPositions   ElementID = 0x7D7B
	IDBitDepth           ElementID = 0x6264
	IDPixelWidth         ElementID = 0xB0
	IDPixelHeight        ElementID = 0xBA
	IDBitsPerChannel     ElementID = 0x55B2
	IDMatrixCoeffs       ElementID = 0x55B1
	IDRange              ElementID = 0x55B9
	IDTransferChars      ElementID = 0x55BA
	IDPrimaries          ElementID = 0x55BB
	IDDisplayWidth       ElementID = 0x54B0
	IDDisplayHeight      ElementID = 0x54BA
	IDDisplayUnit        ElementID = 0x54B2
	IDMaxBlockAdditionID ElementID = 0x55EE

	IDCluster        ElementID = 0x1F43B675
	IDTimestamp      ElementID = 0xE7
	IDBlockGroup     ElementID = 0xA0
	IDBlock          ElementID = 0xA1
	IDSimpleBlock    ElementID = 0xA3
	IDBlockDuration  ElementID = 0x9B
	IDReferenceBlock ElementID = 0xFB

	IDCues        ElementID = 0x1C53BB6B
	IDCuePoint    ElementID = 0xBB
	IDCueTime     ElementID = 0xB3
	IDCueTrackPos ElementID = 0xB7
	IDVoid        ElementID = 0xEC
	IDCRC32       ElementID = 0xBF
	IDChapters    ElementID = 0x1043A770
	IDTags        ElementID = 0x1254C367
	IDAttachments ElementID = 0x1941A469
)

// idNames maps every recognized element ID to its Matroska name.
var idNames = map[ElementID]string{
	IDEBMLHeader: "EBML", IDEBMLVersion: "EBMLVersion", IDEBMLReadVersion: "EBMLReadVersion",
	IDEBMLMaxIDLength: "EBMLMaxIDLength", IDEBMLMaxSizeLen: "EBMLMaxSizeLength",
	IDDocType: "DocType", IDDocTypeVersion: "DocTypeVersion", IDDocTypeReadVer: "DocTypeReadVersion",
	IDSegment: "Segment", IDSeekHead: "SeekHead", IDInfo: "Info",
	IDTimecodeScale: "TimestampScale", IDMuxingApp: "MuxingApp", IDWritingApp: "WritingApp",
	IDDuration: "Duration",
	IDTracks:   "Tracks", IDTrackEntry: "TrackEntry", IDTrackNumber: "TrackNumber",
	IDTrackUID: "TrackUID", IDTrackType: "TrackType", IDFlagDefault: "FlagDefault",
	IDName: "Name", IDLanguage: "Language", IDCodecID: "CodecID",
	IDAudio: "Audio", IDVideo: "Video", IDColour: "Colour",
	IDSamplingFrequency: "SamplingFrequency", IDChannels: "Channels",
	IDChannelPositions: "ChannelPositions", IDBitDepth: "BitDepth",
	IDPixelWidth: "PixelWidth", IDPixelHeight: "PixelHeight",
	IDBitsPerChannel: "BitsPerChannel", IDMatrixCoeffs: "MatrixCoefficients",
	IDRange: "Range", IDTransferChars: "TransferCharacteristics", IDPrimaries: "Primaries",
	IDDisplayWidth: "DisplayWidth", IDDisplayHeight: "DisplayHeight",
	IDDisplayUnit: "DisplayUnit", IDMaxBlockAdditionID: "MaxBlockAdditionID",
	IDCluster: "Cluster", IDTimestamp: "Timestamp", IDBlockGroup: "BlockGroup",
	IDBlock: "Block", IDSimpleBlock: "SimpleBlock", IDBlockDuration: "BlockDuration",
	IDReferenceBlock: "ReferenceBlock",
	IDCues:           "Cues", IDCuePoint: "CuePoint", IDCueTime: "CueTime",
	IDCueTrackPos: "CueTrackPositions", IDVoid: "Void", IDCRC32: "CRC-32",
	IDChapters: "Chapters", IDTags: "Tags", IDAttachments: "Attachments",
}

// containerIDs lists elements whose payload holds nested EBML elements.
var containerIDs = map[ElementID]bool{
	IDEBMLHeader: true, IDSegment: true, IDSeekHead: true, IDInfo: true,
	IDTracks: true, IDTrackEntry: true, IDAudio: true, IDVideo: true,
	IDColour: true, IDCluster: true, IDBlockGroup: true, IDCues: true,
	IDCuePoint: true, IDCueTrackPos: true,
}

// Element describes one parsed EBML element header with exact input offsets.
type Element struct {
	ID            ElementID
	Offset        int64 // absolute offset of the ID VINT
	HeaderSize    int   // ID VINT width + size VINT width
	Size          int64 // declared payload size; -1 when unknown
	UnknownSize   bool
	PayloadOffset int64 // absolute offset where the payload begins
	PayloadEnd    int64 // exclusive absolute end; set at parent/EOF for unknown sizes
}

// Name returns the Matroska name, or a hex fallback for unregistered IDs.
func (e *Element) Name() string { return NameOf(e.ID) }

// IsContainer reports whether the element payload is parsed recursively.
func (e *Element) IsContainer() bool { return containerIDs[e.ID] }

// NameOf returns the registered name of id or a 0x-hex fallback.
func NameOf(id ElementID) string {
	if n, ok := idNames[id]; ok {
		return n
	}
	return "0x" + hexID(id)
}

// IsKnown reports whether id belongs to the registry.
func IsKnown(id ElementID) bool {
	_, ok := idNames[id]
	return ok
}

// ValidEBMLID reports whether id is a legal on-wire EBML element ID: it must
// encode to a canonical 1..4 byte VINT whose length marker is included in the
// stored value (e.g. Segment is 0x18538067, four bytes on the wire). The
// reserved all-ones unknown-length VINT is not a legal element ID.
func ValidEBMLID(id ElementID) bool {
	enc, err := EncodeID(id)
	if err != nil || len(enc) == 0 || len(enc) > 4 {
		return false
	}
	_, _, unknown, derr := DecodeVINT(enc)
	return derr == nil && !unknown
}

// EncodeID emits the on-wire VINT bytes of an element ID whose stored value
// already includes the length marker bit (e.g. SimpleBlock is 0xA3).
func EncodeID(id ElementID) ([]byte, error) {
	v := uint64(id)
	// Emit the big-endian bytes of v, dropping fully zero leading bytes.
	raw := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		raw[i] = byte(v & 0xFF)
		v >>= 8
	}
	first := 0
	for first < 7 && raw[first] == 0 {
		first++
	}
	buf := raw[first:]
	if w, ok := VINTWidth(buf[0]); !ok || w != len(buf) || w > 4 {
		return nil, serr.New(serr.CodeUnknownElement, "0x%X is not a canonical 1..4 byte element ID", uint64(id))
	}
	return buf, nil
}

func hexID(id ElementID) string {
	const digits = "0123456789ABCDEF"
	b, err := EncodeID(id)
	if err != nil {
		return "?"
	}
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&0xF])
	}
	return string(out)
}

// PathString joins element names into a slash separated path.
func PathString(ids []ElementID) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = NameOf(id)
	}
	return joinSlash(names)
}

func joinSlash(parts []string) string {
	n := 0
	for _, p := range parts {
		n += len(p) + 1
	}
	out := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			out = append(out, '/')
		}
		out = append(out, p...)
	}
	return string(out)
}

// errAt wraps a structured error with offset and the current element path.
func errAt(err *serr.Error, offset int64, path []ElementID) *serr.Error {
	names := make([]string, len(path))
	for i, id := range path {
		names[i] = NameOf(id)
	}
	return err.At(offset).WithPath(names)
}
