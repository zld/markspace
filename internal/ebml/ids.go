package ebml

// Matroska/WebM element IDs used by this project's declared subset. IDs
// retain their canonical VINT form (including length-marker bits).
const (
	idEBMLHeader  uint32 = 0x1A45DFA3
	idDocType     uint32 = 0x4282
	idTimeScale   uint32 = 0x2AD7B1
	idMuxingApp   uint32 = 0x4D80
	idWritingApp  uint32 = 0x5741
	idSegment     uint32 = 0x18538067
	idSeekHead    uint32 = 0x114D9B74
	idSeek        uint32 = 0x4DBB
	idInfo        uint32 = 0x1549A966
	idTracks      uint32 = 0x1654AE6B
	idTrackEntry  uint32 = 0xAE
	idTrackNumber uint32 = 0xD7
	idTrackUID    uint32 = 0x73C5
	idCodecID     uint32 = 0x86
	idCluster     uint32 = 0x1F43B675
	idTimecode    uint32 = 0xE7
	idSimpleBlock uint32 = 0xA3
	idBlock       uint32 = 0xA1
	idTags        uint32 = 0x1254C367
	idTag         uint32 = 0x7373
	idCues        uint32 = 0x1C53BB6B
	idCuePoint    uint32 = 0xBB
)

// Public aliases for packages that need to identify blocks and tracks.
const (
	IDSimpleBlock = idSimpleBlock
	IDBlock       = idBlock
	IDCluster     = idCluster
	IDTimecode    = idTimecode
	IDTrackNumber = idTrackNumber
	IDTrackEntry  = idTrackEntry
	IDSegment     = idSegment
	IDInfo        = idInfo
	IDTimeScale   = idTimeScale
	IDDocType     = idDocType
)
