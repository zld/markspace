package ebml

var idNames = map[ElementID]string{
	IDEBML: "EBML", IDEBMLVersion: "EBMLVersion", IDEBMLReadVersion: "EBMLReadVersion",
	IDEBMLMaxIDLength: "EBMLMaxIDLength", IDEBMLMaxSizeLen: "EBMLMaxSizeLength",
	IDDocType: "DocType", IDDocTypeVersion: "DocTypeVersion",
	IDDocTypeReadVer: "DocTypeReadVersion", IDSegment: "Segment", IDInfo: "Info",
	IDTimecodeScale: "TimecodeScale", IDMuxingApp: "MuxingApp", IDWritingApp: "WritingApp",
	IDTracks: "Tracks", IDTrackEntry: "TrackEntry", IDTrackNumber: "TrackNumber",
	IDTrackUID: "TrackUID", IDTrackType: "TrackType", IDCodecID: "CodecID",
	IDName: "Name", IDCluster: "Cluster", IDTimecode: "Timecode",
	IDSimpleBlock: "SimpleBlock", IDBlockGroup: "BlockGroup", IDBlock: "Block",
	IDVoid: "Void", IDCRC32: "CRC-32",
}

// Name returns the symbolic element name, or a 0x hex id for unknown ones.
func (id ElementID) Name() string {
	if n, ok := idNames[id]; ok {
		return n
	}
	return "0x" + bytesToHex(uint64(id))
}
