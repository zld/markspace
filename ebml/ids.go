package ebml

// Matroska-flavoured EBML element identifiers used by the fixture format.
// Standard Matroska IDs are kept for common elements; IDs in the 0x66xx and
// 0x55B9 ranges are test-harness extensions and never claim standard meaning.
const (
	IDEBMLHeader     ElementID = 0x1A45DFA3
	IDDocType        ElementID = 0x4282
	IDSegment        ElementID = 0x18538067
	IDInfo           ElementID = 0x1549A966
	IDTimeScale      ElementID = 0x2AD7B1
	IDDuration       ElementID = 0x4489
	IDContainerCol   ElementID = 0x55B9 // harness: container-level colour record
	IDTracks         ElementID = 0x1654AE6B
	IDTrackEntry     ElementID = 0xAE
	IDTrackNumber    ElementID = 0xD7
	IDTrackUID       ElementID = 0x73C5
	IDTrackType      ElementID = 0x83
	IDCodecID        ElementID = 0x86
	IDDefaultDur     ElementID = 0x23E383
	IDPixelWidth     ElementID = 0xB0
	IDPixelHeight    ElementID = 0xBA
	IDColour         ElementID = 0x55B0
	IDColorMatrix    ElementID = 0x55B1
	IDColorTransfer  ElementID = 0x55B2
	IDColorPrimaries ElementID = 0x55B3
	IDColorRange     ElementID = 0x55B5
	IDColorRaw       ElementID = 0x55B6 // harness: unknown enumeration, retained
	IDSamplingFreq   ElementID = 0xB5
	IDChannels       ElementID = 0x9F
	IDBitDepth       ElementID = 0x6264
	IDChanLayout     ElementID = 0x66C0 // harness: layout name string
	IDChanMap        ElementID = 0x66C1 // harness: ordered position table
	IDCluster        ElementID = 0x1F43B675
	IDClusterTime    ElementID = 0xE7
	IDSimpleBlock    ElementID = 0xA3
	IDCRC32          ElementID = 0xBF // harness: CRC-32 of cluster children
)

// Track types per the fixture convention.
const (
	TrackTypeVideo = 1
	TrackTypeAudio = 2
)
