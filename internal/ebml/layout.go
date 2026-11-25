package ebml

import "fmt"

// legalChildren is enforced for the masters whose internal layout this tool
// validates. Masters not listed here accept any child so forward-compatible
// metadata does not abort delivery.
var legalChildren = map[uint32]map[uint32]bool{
	IDEBML: {
		IDEBMLVersion: true, IDEBMLReadVersion: true, IDEBMLMaxIDLength: true,
		IDEBMLMaxSizeLength: true, IDDocType: true, IDDocTypeVersion: true,
		IDDocTypeReadVersion: true, IDVoid: true, IDCRC32: true,
	},
	IDSegment: {
		IDSeekHead: true, IDInfo: true, IDTracks: true, IDCluster: true,
		IDCues: true, IDAttachments: true, IDChapters: true, IDTags: true,
		IDVoid: true, IDCRC32: true,
	},
	IDInfo: {
		IDSegmentUID: true, IDTimecodeScale: true, IDDuration: true,
		IDMuxingApp: true, IDWritingApp: true, IDTitle: true, IDVoid: true,
		IDCRC32: true,
	},
	IDTracks:     {IDTrackEntry: true, IDVoid: true, IDCRC32: true},
	IDTrackEntry: trackEntryChildren,
	IDAudio: {
		IDSamplingFrequency: true, IDOutputSamplingFreq: true, IDChannels: true,
		IDBitDepth: true, IDVoid: true, IDCRC32: true,
	},
	IDVideo: {
		IDPixelWidth: true, IDPixelHeight: true, IDDisplayWidth: true,
		IDDisplayHeight: true, IDColour: true, IDVoid: true, IDCRC32: true,
	},
	IDColour: {
		IDMatrixCoefficients: true, IDBitsPerChannel: true,
		IDChromaSubsampleHorz: true, IDChromaSubsampleVert: true,
		IDCbSubsampleHorz: true, IDCbSubsampleVert: true,
		IDChromaSitingHorz: true, IDChromaSitingVert: true, IDRange: true,
		IDTransferChars: true, IDPrimaries: true, IDMaxCLL: true,
		IDMaxFALL: true, IDMasteringMetadata: true, IDVoid: true,
	},
	IDMasteringMetadata: {IDLuminanceMax: true, IDLuminanceMin: true, IDVoid: true},
	IDCluster: {
		IDTimecode: true, IDSimpleBlock: true, IDBlockGroup: true,
		IDVoid: true, IDCRC32: true,
	},
	IDBlockGroup: {IDBlock: true, IDBlockDuration: true, IDReferenceBlock: true, IDVoid: true},
	IDCues:       {IDCuePoint: true, IDVoid: true, IDCRC32: true},
	IDCuePoint:   {IDCueTime: true, IDCueTrackPositions: true, IDVoid: true},
}

var trackEntryChildren = map[uint32]bool{
	IDTrackNumber: true, IDTrackUID: true, IDTrackType: true,
	IDFlagEnabled: true, IDFlagDefault: true, IDFlagForced: true,
	IDFlagLacing: true, IDCodecID: true, IDCodecPrivate: true,
	IDCodecName: true, IDDefaultDuration: true, IDLanguage: true,
	IDName: true, IDAudio: true, IDVideo: true, IDVoid: true, IDCRC32: true,
}

// Name returns the human-readable Matroska name for a known ID.
func Name(id uint32) string {
	if d, ok := defs[id]; ok {
		return d.name
	}
	return fmt.Sprintf("Unknown(0x%X)", id)
}

// IsMaster reports whether the ID opens a master element.
func IsMaster(id uint32) bool {
	d, ok := defs[id]
	return ok && d.master
}

// Level returns the nesting level used for unknown-length boundary rules.
// Unknown IDs return -1.
func Level(id uint32) int {
	if d, ok := defs[id]; ok {
		return d.level
	}
	return -1
}

// Known reports whether the ID is in the supported Matroska subset.
func Known(id uint32) bool {
	_, ok := defs[id]
	return ok
}

// LegalChild reports whether child may appear directly inside parent. Unknown
// parents and unknown children are accepted to remain forward compatible.
func LegalChild(parent, child uint32) bool {
	set, ok := legalChildren[parent]
	if !ok {
		return true
	}
	if set[child] {
		return true
	}
	// Global elements are legal in every master.
	if child == IDVoid || child == IDCRC32 {
		return true
	}
	// Unknown extension IDs are tolerated; known IDs in the wrong place are not.
	return !Known(child)
}

// AllowsUnknownSize reports whether this master may use unknown payload size
// at the given parent.
func AllowsUnknownSize(id, parent uint32) bool {
	switch id {
	case IDSegment:
		return parent == 0
	case IDCluster:
		return parent == IDSegment
	default:
		return false
	}
}

// IsLevelSibling reports whether id starts a new element at the level of an
// open unknown-length frame, which forces that frame to close.
func IsLevelSibling(frameID, id uint32) bool {
	switch frameID {
	case IDCluster:
		l := Level(id)
		return l == levelSegment || l == levelTop
	case IDSegment:
		return Level(id) == levelTop
	default:
		return false
	}
}
