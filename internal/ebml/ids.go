// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file declares the element ids the pipeline
// understands.
package ebml

// ID is an EBML element id including its length marker.
type ID uint32

// Matroska/WebM element ids the pipeline reads.
const (
	IDEBML          ID = 0x1A45DFA3
	IDDocType       ID = 0x4282
	IDSegment       ID = 0x18538067
	IDInfo          ID = 0x1549A966
	IDTimecodeScale ID = 0x2AD7B1
	IDTracks        ID = 0x1654AE6B
	IDTrackEntry    ID = 0xAE
	IDTrackNumber   ID = 0xD7
	IDTrackUID      ID = 0x73C5
	IDTrackType     ID = 0x83
	IDCodecID       ID = 0x86
	IDDefaultDur    ID = 0x23E383
	IDVideo         ID = 0xE0
	IDAudio         ID = 0xE1
	IDCluster       ID = 0x1F43B675
	IDTimecode      ID = 0xE7
	IDSimpleBlock   ID = 0xA3
	IDBlockGroup    ID = 0xA0
	IDBlock         ID = 0xA1
	IDCues          ID = 0x1C53BB6B
	IDCuePoint      ID = 0xBB
	IDCueTrackPos   ID = 0xB7
	IDSeekHead      ID = 0x114D9B74
	IDSeek          ID = 0x4DBB
	IDTags          ID = 0x1254C367
	IDTag           ID = 0x7373
	IDTargets       ID = 0x63C0
	IDSimpleTag     ID = 0x67C8
	IDChapters      ID = 0x1043A770
	IDEditionEntry  ID = 0x45B9
	IDChapterAtom   ID = 0xB6
	IDAttachments   ID = 0x1941A469
	IDAttachedFile  ID = 0x61A7
	IDVoid          ID = 0xEC
	IDCRC32         ID = 0xBF
	IDPosition      ID = 0xA7
	IDPrevSize      ID = 0xAB
	IDEncryptedBlk  ID = 0xAF
	IDBlockDuration ID = 0x9B
	IDReferenceBlk  ID = 0xFB
	IDContentEnc    ID = 0x6D80
	IDContentEncod  ID = 0x6240
	IDPixelWidth    ID = 0xB0
	IDPixelHeight   ID = 0xBA
	IDSamplingFreq  ID = 0xB5
	IDChannels      ID = 0x9F
	IDMuxingApp     ID = 0x4D80
	IDWritingApp    ID = 0x5741
	IDDuration      ID = 0x4489
	IDFlagDefault   ID = 0x88
	IDCodecPrivate  ID = 0x63A2
	IDName          ID = 0x536E
	IDLanguage      ID = 0x22B59C
)
