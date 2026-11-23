// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file describes which elements are masters
// and which children may appear inside them.
package ebml

// masters is the set of element ids treated as master elements.
var masters = map[ID]bool{
	IDEBML: true, IDSegment: true, IDInfo: true, IDTracks: true,
	IDTrackEntry: true, IDVideo: true, IDAudio: true,
	IDCluster: true, IDBlockGroup: true, IDCues: true, IDCuePoint: true,
	IDCueTrackPos: true, IDSeekHead: true, IDSeek: true, IDTags: true,
	IDTag: true, IDTargets: true, IDSimpleTag: true, IDChapters: true,
	IDEditionEntry: true, IDChapterAtom: true, IDAttachments: true,
	IDAttachedFile: true, IDContentEnc: true, IDContentEncod: true,
}

// IsMaster reports whether id denotes a master element.
func IsMaster(id ID) bool { return masters[id] }

// unknownOK lists masters that may omit their size.
var unknownOK = map[ID]bool{
	IDSegment: true, IDCluster: true, IDBlockGroup: true,
}

// children lists the ids that may appear directly inside a master. It is used
// only to find the end of an unknown-size master.
var children = map[ID]map[ID]bool{
	IDSegment: setOf(IDSeekHead, IDInfo, IDTracks, IDCluster, IDCues,
		IDAttachments, IDChapters, IDTags, IDVoid, IDCRC32),
	IDCluster: setOf(IDTimecode, IDSimpleBlock, IDBlockGroup, IDPosition,
		IDPrevSize, IDEncryptedBlk, IDVoid, IDCRC32),
	IDBlockGroup: setOf(IDBlock, IDBlockDuration, IDReferenceBlk, IDVoid, IDCRC32),
	IDInfo: setOf(IDTimecodeScale, IDDuration, IDMuxingApp, IDWritingApp,
		IDVoid, IDCRC32),
	IDTracks: setOf(IDTrackEntry, IDVoid, IDCRC32),
	IDTrackEntry: setOf(IDTrackNumber, IDTrackUID, IDTrackType, IDFlagDefault,
		IDCodecID, IDCodecPrivate, IDName, IDLanguage, IDDefaultDur,
		IDVideo, IDAudio, IDContentEnc, IDVoid, IDCRC32),
	IDVideo: setOf(IDPixelWidth, IDPixelHeight, IDVoid, IDCRC32),
	IDAudio: setOf(IDSamplingFreq, IDChannels, IDVoid, IDCRC32),
	IDEBML:  setOf(IDDocType, IDVoid, IDCRC32),
}

func setOf(ids ...ID) map[ID]bool {
	m := make(map[ID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// validChild reports whether id may start directly inside parent. Unknown
// parents accept anything.
func validChild(parent, id ID) bool {
	if id == IDVoid || id == IDCRC32 {
		return true
	}
	set, ok := children[parent]
	if !ok {
		return true
	}
	return set[id]
}
