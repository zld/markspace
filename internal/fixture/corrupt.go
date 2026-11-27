package fixture

import (
	"tracecheck/internal/ebml"
)

// truncatedBlockData builds a healthy WebM and appends a SimpleBlock header
// that claims 64 payload bytes but supplies only two. The healthy prefix is
// preserved so the reported error offset points past valid data.
func truncatedBlockData() []byte {
	base := buildWebM(DefaultSpec(), true, true)
	idBytes, _ := ebml.EncodeID(ebml.IDSimpleBlock)
	sizeBytes, _ := ebml.EncodeVINT(64)
	junk := append(append(append([]byte{}, idBytes...), sizeBytes...), 0x81, 0x00)
	return append(append([]byte{}, base...), junk...)
}

// boundaryConflictData builds a WebM whose unknown-length Segment holds an
// unknown-length Cluster followed by a TrackEntry. TrackEntry is legal inside
// Tracks only, so it can close neither Cluster nor Segment and the parser
// reports PARENT_BOUNDARY_CONFLICT.
func boundaryConflictData() []byte {
	var entry ebml.Builder
	_ = entry.UintElement(ebml.IDTrackNumber, 7)
	var stray ebml.Builder
	_ = stray.Element(ebml.IDTrackEntry, entry.Bytes())

	var seg ebml.Builder
	var cluster ebml.Builder
	_ = cluster.UintElement(ebml.IDTimestamp, 0)
	_ = cluster.Element(ebml.IDSimpleBlock, simpleBlockPayload(1, 0, true, mediaFrame(0, 32)))
	_ = seg.UnknownElement(ebml.IDCluster, cluster.Bytes())
	seg.WriteRaw(stray.Bytes())

	var top ebml.Builder
	_ = top.UnknownElement(ebml.IDSegment, seg.Bytes())
	return top.Bytes()
}

// metadataConflictData builds a WebM with a well-formed video track and an
// audio track that declares Audio/Channels twice with different values. The
// first value is kept, the second is recorded as a conflict, and the media
// bytes are untouched.
func metadataConflictData() []byte {
	var info ebml.Builder
	_ = info.UintElement(ebml.IDTimecodeScale, 1_000_000)

	var tracks ebml.Builder
	_ = tracks.Container(ebml.IDTrackEntry, func(e *ebml.Builder) error {
		_ = e.UintElement(ebml.IDTrackNumber, 1)
		_ = e.UintElement(ebml.IDTrackType, 1)
		_ = e.StringElement(ebml.IDCodecID, "V_VP8")
		return e.Container(ebml.IDVideo, func(v *ebml.Builder) error {
			_ = v.UintElement(ebml.IDPixelWidth, 320)
			return v.UintElement(ebml.IDPixelHeight, 180)
		})
	})
	_ = tracks.Container(ebml.IDTrackEntry, func(e *ebml.Builder) error {
		_ = e.UintElement(ebml.IDTrackNumber, 2)
		_ = e.UintElement(ebml.IDTrackType, 2)
		_ = e.StringElement(ebml.IDCodecID, "A_OPUS")
		return e.Container(ebml.IDAudio, func(a *ebml.Builder) error {
			_ = a.FloatElement(ebml.IDSamplingFrequency, 48000)
			_ = a.UintElement(ebml.IDChannels, 2)    // kept
			return a.UintElement(ebml.IDChannels, 6) // conflicting
		})
	})

	var seg ebml.Builder
	_ = seg.Element(ebml.IDInfo, info.Bytes())
	_ = seg.Element(ebml.IDTracks, tracks.Bytes())
	var cluster ebml.Builder
	_ = cluster.UintElement(ebml.IDTimestamp, 0)
	_ = cluster.Element(ebml.IDSimpleBlock,
		simpleBlockPayload(2, 0, false, mediaFrame(0, 48)))
	_ = seg.Element(ebml.IDCluster, cluster.Bytes())

	var top ebml.Builder
	_ = top.Container(ebml.IDEBMLHeader, func(h *ebml.Builder) error {
		return h.StringElement(ebml.IDDocType, "webm")
	})
	_ = top.Element(ebml.IDSegment, seg.Bytes())
	return top.Bytes()
}
