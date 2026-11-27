// Package fixture generates self-contained Matroska/WebM subset files for
// testing. No external data is referenced: every byte is synthesized,
// including valid RTP packets carried in SimpleBlock payloads.
package fixture

import (
	"github.com/local/tracecheck/internal/ebml"
	"github.com/pion/rtp"
)

// IDs reused by the generator.
const (
	idEBMLHeader  = 0x1A45DFA3
	idDocType     = 0x4282
	idTimeScale   = 0x2AD7B1
	idSegment     = 0x18538067
	idInfo        = 0x1549A966
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackNumber = 0xD7
	idTrackUID    = 0x73C5
	idCodecID     = 0x86
	idCluster     = 0x1F43B675
	idTimecode    = 0xE7
	idSimpleBlock = 0xA3
)

// Options controls the valid fixture.
type Options struct {
	Tracks      int
	Clusters    int
	BlocksPerCl int
	SegmentNS   int64 // nanoseconds per block step
	TimeScaleNS uint64
}

// DefaultOptions returns the options used by the acceptance fixtures.
func DefaultOptions() Options {
	return Options{
		Tracks: 2, Clusters: 6, BlocksPerCl: 2,
		SegmentNS: 20_000_000, TimeScaleNS: 1_000_000,
	}
}

// rtpBlockPayload builds a SimpleBlock payload wrapping an RTP packet.
func rtpBlockPayload(track uint64, rel int16, seq uint16) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 111, SequenceNumber: seq,
			Timestamp: uint32(seq) * 160, SSRC: 7,
		},
		Payload: []byte{0x10, 0x20, 0x30},
	}
	raw, _ := pkt.Marshal()
	out := append(ebml.EncodeVINT(track), byte(int32(rel)>>8), byte(rel), 0x00)
	return append(out, raw...)
}

func u64(v uint64) []byte {
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

func trackEntry(num uint64, codec string) []byte {
	tn := ebml.EncodeElement(idTrackNumber, []byte{byte(num)})
	uid := ebml.EncodeElement(idTrackUID, u64(num))
	cid := ebml.EncodeElement(idCodecID, []byte(codec))
	return ebml.EncodeElement(idTrackEntry, append(append(tn, uid...), cid...))
}

// Valid builds a well-formed interleaved multi-track WebM file.
func Valid(opts Options) []byte {
	header := ebml.EncodeElement(idEBMLHeader,
		append(ebml.EncodeElement(idDocType, []byte("webm")),
			ebml.EncodeElement(idTimeScale, u64(opts.TimeScaleNS))...))

	var tracks []byte
	for t := 1; t <= opts.Tracks; t++ {
		tracks = append(tracks, trackEntry(uint64(t), "A_OPUS")...)
	}
	tracksEl := ebml.EncodeElement(idTracks, tracks)
	infoEl := ebml.EncodeElement(idInfo,
		ebml.EncodeElement(idTimeScale, u64(opts.TimeScaleNS)))

	segBody := append(infoEl, tracksEl...)
	seq := uint16(0)
	for c := 0; c < opts.Clusters; c++ {
		clusterTC := int64(c) * opts.SegmentNS * int64(opts.BlocksPerCl) / int64(opts.TimeScaleNS)
		var cluster []byte
		cluster = append(cluster, ebml.EncodeElement(idTimecode, u64(uint64(clusterTC)))...)
		for b := 0; b < opts.BlocksPerCl; b++ {
			for t := 1; t <= opts.Tracks; t++ {
				rel := int16(int64(b)*opts.SegmentNS/int64(opts.TimeScaleNS) + int64(t-1))
				cluster = append(cluster, ebml.EncodeElement(idSimpleBlock,
					rtpBlockPayload(uint64(t), rel, seq))...)
				seq++
			}
		}
		segBody = append(segBody, ebml.EncodeElement(idCluster, cluster)...)
	}
	return append(header, ebml.EncodeElement(idSegment, segBody)...)
}

// TruncatedUnknownLength builds a document whose final cluster uses an
// unknown-length size VINT but whose payload is cut mid-child. The returned
// Offset is the first byte of the malformed child header.
func TruncatedUnknownLength() (data []byte, offset int64) {
	header := ebml.EncodeElement(idEBMLHeader, ebml.EncodeElement(idDocType, []byte("webm")))
	// Known child followed by an unknown-length cluster containing a
	// truncated child header (declared size exceeds available bytes).
	good := ebml.EncodeElement(idTimecode, u64(0))
	badChild := append(ebml.EncodeID(idSimpleBlock), ebml.EncodeVINT(50)...)
	badChild = append(badChild, 1, 2) // far fewer than 50 payload bytes
	cluster := ebml.EncodeUnknownElement(idCluster, 1, append(good, badChild...))
	seg := ebml.EncodeElement(idSegment, cluster)
	doc := append(header, seg...)
	offset = int64(len(doc)) - int64(len(badChild))
	return doc, offset
}

// IllegalVINT builds a document containing a reserved all-ones element ID
// (0xFF). The returned offset points at the illegal byte.
func IllegalVINT() (data []byte, offset int64) {
	header := ebml.EncodeElement(idEBMLHeader, ebml.EncodeElement(idDocType, []byte("webm")))
	// A segment whose first child starts with the illegal 0xFF ID marker.
	seg := ebml.EncodeElement(idSegment, []byte{0xFF, 0x81, 0x00})
	doc := append(header, seg...)
	offset = int64(len(doc)) - 3
	return doc, offset
}

// TimeRegression builds a syntactically valid file in which one track's
// block timecode moves backwards relative to its predecessor.
func TimeRegression() (data []byte, offset int64) {
	header := ebml.EncodeElement(idEBMLHeader, ebml.EncodeElement(idDocType, []byte("webm")))
	tracks := ebml.EncodeElement(idTracks, trackEntry(1, "A_OPUS"))
	info := ebml.EncodeElement(idInfo, ebml.EncodeElement(idTimeScale, u64(1_000_000)))
	mkBlock := func(rel int16, seq uint16) []byte {
		return ebml.EncodeElement(idSimpleBlock, rtpBlockPayload(1, rel, seq))
	}
	blocks := mkBlock(100, 0)
	regressing := mkBlock(50, 1) // backwards from 100 to 50
	cluster := ebml.EncodeElement(idCluster,
		append(append(ebml.EncodeElement(idTimecode, u64(0)), blocks...), regressing...))
	seg := ebml.EncodeElement(idSegment, append(append(info, tracks...), cluster...))
	doc := append(header, seg...)
	offset = int64(len(doc)) - int64(len(regressing))
	return doc, offset
}
