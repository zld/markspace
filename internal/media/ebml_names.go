package media

import "strconv"

// Matroska/WebM element IDs used by the generated fixtures and extractor.
const (
	IDEBMLEBML           uint32 = 0x1A45DFA3
	IDDocType            uint32 = 0x4282
	IDSegment            uint32 = 0x18538067
	IDInfo               uint32 = 0x1549A966
	IDTimecodeScale      uint32 = 0x2AD7B1
	IDMuxingApp          uint32 = 0x4D80
	IDWritingApp         uint32 = 0x5741
	IDTracks             uint32 = 0x1654AE6B
	IDTrackEntry         uint32 = 0xAE
	IDTrackNumber        uint32 = 0xD7
	IDTrackUID           uint32 = 0x73C5
	IDTrackType          uint32 = 0x83
	IDCodecID            uint32 = 0x86
	IDAudio              uint32 = 0xE1
	IDChannels           uint32 = 0x9F
	IDSamplingFrequency  uint32 = 0xB5
	IDChannelLayout      uint32 = 0x4371 // project fixture extension: layout string
	IDVideo              uint32 = 0xE0
	IDColour             uint32 = 0x55B0
	IDMatrixCoefficients uint32 = 0x55B1
	IDColourRange        uint32 = 0x55B9
	IDTransferCharacter  uint32 = 0x55BA
	IDColourPrimaries    uint32 = 0x55BB
	IDCluster            uint32 = 0x1F43B675
	IDTimecode           uint32 = 0xE7
	IDBlockGroup         uint32 = 0xA0
	IDBlock              uint32 = 0xA1
	IDBlockDuration      uint32 = 0x9B
	IDSimpleBlock        uint32 = 0xA3
)

var ebmlNames = map[uint32]string{
	IDEBMLEBML:           "EBML",
	IDDocType:            "DocType",
	IDSegment:            "Segment",
	IDInfo:               "Info",
	IDTimecodeScale:      "TimecodeScale",
	IDMuxingApp:          "MuxingApp",
	IDWritingApp:         "WritingApp",
	IDTracks:             "Tracks",
	IDTrackEntry:         "TrackEntry",
	IDTrackNumber:        "TrackNumber",
	IDTrackUID:           "TrackUID",
	IDTrackType:          "TrackType",
	IDCodecID:            "CodecID",
	IDAudio:              "Audio",
	IDChannels:           "Channels",
	IDSamplingFrequency:  "SamplingFrequency",
	IDChannelLayout:      "X-ChannelLayout",
	IDVideo:              "Video",
	IDColour:             "Colour",
	IDMatrixCoefficients: "MatrixCoefficients",
	IDColourRange:        "ColourRange",
	IDTransferCharacter:  "TransferCharacteristics",
	IDColourPrimaries:    "ColourPrimaries",
	IDCluster:            "Cluster",
	IDTimecode:           "Timecode",
	IDBlockGroup:         "BlockGroup",
	IDBlock:              "Block",
	IDBlockDuration:      "BlockDuration",
	IDSimpleBlock:        "SimpleBlock",
}

var containerIDs = map[uint32]bool{
	IDEBMLEBML:   true,
	IDSegment:    true,
	IDInfo:       true,
	IDTracks:     true,
	IDTrackEntry: true,
	IDAudio:      true,
	IDVideo:      true,
	IDColour:     true,
	IDCluster:    true,
	IDBlockGroup: true,
}

// EBMLName returns a stable path component for an element ID.
func EBMLName(id uint32) string {
	if name, ok := ebmlNames[id]; ok {
		return name
	}
	return "Element_" + strconv.FormatUint(uint64(id), 16)
}

// IsContainerID reports whether the fixture parser should recurse into id.
func IsContainerID(id uint32) bool { return containerIDs[id] }
