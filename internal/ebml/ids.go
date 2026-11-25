package ebml

// Matroska element IDs are stored with their length-marker bits, exactly as
// they appear on disk, so an error can print the same 0x... value a hex dump
// shows. Only the subset needed for offline delivery validation is listed.
const (
	IDEBML                uint32 = 0x1A45DFA3
	IDEBMLVersion         uint32 = 0x4286
	IDEBMLReadVersion     uint32 = 0x42F7
	IDEBMLMaxIDLength     uint32 = 0x42F2
	IDEBMLMaxSizeLength   uint32 = 0x42F3
	IDDocType             uint32 = 0x4282
	IDDocTypeVersion      uint32 = 0x4287
	IDDocTypeReadVersion  uint32 = 0x4285
	IDSegment             uint32 = 0x18538067
	IDSeekHead            uint32 = 0x114D9B74
	IDSeek                uint32 = 0x4DBB
	IDSeekID              uint32 = 0x53AB
	IDSeekPosition        uint32 = 0x53AC
	IDInfo                uint32 = 0x1549A966
	IDSegmentUID          uint32 = 0x73A4
	IDTimecodeScale       uint32 = 0x2AD7B1
	IDDuration            uint32 = 0x4489
	IDMuxingApp           uint32 = 0x4D80
	IDWritingApp          uint32 = 0x5741
	IDTitle               uint32 = 0x7BA9
	IDTracks              uint32 = 0x1654AE6B
	IDTrackEntry          uint32 = 0xAE
	IDTrackNumber         uint32 = 0xD7
	IDTrackUID            uint32 = 0x73C5
	IDTrackType           uint32 = 0x83
	IDFlagEnabled         uint32 = 0xB9
	IDFlagDefault         uint32 = 0x88
	IDFlagForced          uint32 = 0x55AA
	IDFlagLacing          uint32 = 0x9C
	IDCodecID             uint32 = 0x86
	IDCodecPrivate        uint32 = 0x63A2
	IDCodecName           uint32 = 0x258688
	IDDefaultDuration     uint32 = 0x23E383
	IDLanguage            uint32 = 0x22B59C
	IDName                uint32 = 0x536E
	IDAudio               uint32 = 0xE1
	IDSamplingFrequency   uint32 = 0xB5
	IDOutputSamplingFreq  uint32 = 0x78B5
	IDChannels            uint32 = 0x9F
	IDBitDepth            uint32 = 0x6264
	IDVideo               uint32 = 0xE0
	IDPixelWidth          uint32 = 0xB0
	IDPixelHeight         uint32 = 0xBA
	IDDisplayWidth        uint32 = 0x54B0
	IDDisplayHeight       uint32 = 0x54BA
	IDColour              uint32 = 0x55B0
	IDMatrixCoefficients  uint32 = 0x55B1
	IDBitsPerChannel      uint32 = 0x55B2
	IDChromaSubsampleHorz uint32 = 0x55B3
	IDChromaSubsampleVert uint32 = 0x55B4
	IDCbSubsampleHorz     uint32 = 0x55B5
	IDCbSubsampleVert     uint32 = 0x55B6
	IDChromaSitingHorz    uint32 = 0x55B7
	IDChromaSitingVert    uint32 = 0x55B8
	IDRange               uint32 = 0x55B9
	IDTransferChars       uint32 = 0x55BA
	IDPrimaries           uint32 = 0x55BB
	IDMaxCLL              uint32 = 0x55BC
	IDMaxFALL             uint32 = 0x55BD
	IDMasteringMetadata   uint32 = 0x55D0
	IDLuminanceMax        uint32 = 0x55D9
	IDLuminanceMin        uint32 = 0x55DA
	IDCluster             uint32 = 0x1F43B675
	IDTimecode            uint32 = 0xE7
	IDSimpleBlock         uint32 = 0xA3
	IDBlockGroup          uint32 = 0xA0
	IDBlock               uint32 = 0xA1
	IDBlockDuration       uint32 = 0x9B
	IDReferenceBlock      uint32 = 0xFB
	IDCues                uint32 = 0x1C53BB6B
	IDCuePoint            uint32 = 0xBB
	IDCueTime             uint32 = 0xB3
	IDCueTrackPositions   uint32 = 0xB7
	IDCueTrack            uint32 = 0xF7
	IDCueClusterPosition  uint32 = 0xF1
	IDAttachments         uint32 = 0x1941A469
	IDChapters            uint32 = 0x1043A770
	IDTags                uint32 = 0x1254C367
	IDVoid                uint32 = 0xEC
	IDCRC32               uint32 = 0xBF
)

type elementDef struct {
	name   string
	master bool
	level  int // for unknown-length boundary decisions; -1 for leaves
}

// Segment and Cluster are the only Matroska masters that may declare an
// unknown payload size; their unknown-length boundary is defined by the next
// sibling element at the parent level.
const (
	levelTop     = 0
	levelSegment = 1
	levelBlock   = 2
)

var defs = map[uint32]elementDef{
	IDEBML:                {"EBML", true, levelTop},
	IDEBMLVersion:         {"EBMLVersion", false, -1},
	IDEBMLReadVersion:     {"EBMLReadVersion", false, -1},
	IDEBMLMaxIDLength:     {"EBMLMaxIDLength", false, -1},
	IDEBMLMaxSizeLength:   {"EBMLMaxSizeLength", false, -1},
	IDDocType:             {"DocType", false, -1},
	IDDocTypeVersion:      {"DocTypeVersion", false, -1},
	IDDocTypeReadVersion:  {"DocTypeReadVersion", false, -1},
	IDSegment:             {"Segment", true, levelTop},
	IDSeekHead:            {"SeekHead", true, levelSegment},
	IDSeek:                {"Seek", true, levelBlock},
	IDSeekID:              {"SeekID", false, -1},
	IDSeekPosition:        {"SeekPosition", false, -1},
	IDInfo:                {"Info", true, levelSegment},
	IDSegmentUID:          {"SegmentUID", false, -1},
	IDTimecodeScale:       {"TimecodeScale", false, -1},
	IDDuration:            {"Duration", false, -1},
	IDMuxingApp:           {"MuxingApp", false, -1},
	IDWritingApp:          {"WritingApp", false, -1},
	IDTitle:               {"Title", false, -1},
	IDTracks:              {"Tracks", true, levelSegment},
	IDTrackEntry:          {"TrackEntry", true, levelBlock},
	IDTrackNumber:         {"TrackNumber", false, -1},
	IDTrackUID:            {"TrackUID", false, -1},
	IDTrackType:           {"TrackType", false, -1},
	IDFlagEnabled:         {"FlagEnabled", false, -1},
	IDFlagDefault:         {"FlagDefault", false, -1},
	IDFlagForced:          {"FlagForced", false, -1},
	IDFlagLacing:          {"FlagLacing", false, -1},
	IDCodecID:             {"CodecID", false, -1},
	IDCodecPrivate:        {"CodecPrivate", false, -1},
	IDCodecName:           {"CodecName", false, -1},
	IDDefaultDuration:     {"DefaultDuration", false, -1},
	IDLanguage:            {"Language", false, -1},
	IDName:                {"Name", false, -1},
	IDAudio:               {"Audio", true, levelBlock},
	IDSamplingFrequency:   {"SamplingFrequency", false, -1},
	IDOutputSamplingFreq:  {"OutputSamplingFrequency", false, -1},
	IDChannels:            {"Channels", false, -1},
	IDBitDepth:            {"BitDepth", false, -1},
	IDVideo:               {"Video", true, levelBlock},
	IDPixelWidth:          {"PixelWidth", false, -1},
	IDPixelHeight:         {"PixelHeight", false, -1},
	IDDisplayWidth:        {"DisplayWidth", false, -1},
	IDDisplayHeight:       {"DisplayHeight", false, -1},
	IDColour:              {"Colour", true, levelBlock},
	IDMatrixCoefficients:  {"MatrixCoefficients", false, -1},
	IDBitsPerChannel:      {"BitsPerChannel", false, -1},
	IDChromaSubsampleHorz: {"ChromaSubsamplingHorz", false, -1},
	IDChromaSubsampleVert: {"ChromaSubsamplingVert", false, -1},
	IDCbSubsampleHorz:     {"CbSubsamplingHorz", false, -1},
	IDCbSubsampleVert:     {"CbSubsamplingVert", false, -1},
	IDChromaSitingHorz:    {"ChromaSitingHorz", false, -1},
	IDChromaSitingVert:    {"ChromaSitingVert", false, -1},
	IDRange:               {"Range", false, -1},
	IDTransferChars:       {"TransferCharacteristics", false, -1},
	IDPrimaries:           {"Primaries", false, -1},
	IDMaxCLL:              {"MaxCLL", false, -1},
	IDMaxFALL:             {"MaxFALL", false, -1},
	IDMasteringMetadata:   {"MasteringMetadata", true, levelBlock},
	IDLuminanceMax:        {"LuminanceMax", false, -1},
	IDLuminanceMin:        {"LuminanceMin", false, -1},
	IDCluster:             {"Cluster", true, levelSegment},
	IDTimecode:            {"Timecode", false, -1},
	IDSimpleBlock:         {"SimpleBlock", false, -1},
	IDBlockGroup:          {"BlockGroup", true, levelBlock},
	IDBlock:               {"Block", false, -1},
	IDBlockDuration:       {"BlockDuration", false, -1},
	IDReferenceBlock:      {"ReferenceBlock", false, -1},
	IDCues:                {"Cues", true, levelSegment},
	IDCuePoint:            {"CuePoint", true, levelBlock},
	IDCueTime:             {"CueTime", false, -1},
	IDCueTrackPositions:   {"CueTrackPositions", true, levelBlock},
	IDCueTrack:            {"CueTrack", false, -1},
	IDCueClusterPosition:  {"CueClusterPosition", false, -1},
	IDAttachments:         {"Attachments", true, levelSegment},
	IDChapters:            {"Chapters", true, levelSegment},
	IDTags:                {"Tags", true, levelSegment},
	IDVoid:                {"Void", false, -1},
	IDCRC32:               {"CRC32", false, -1},
}
