// Package fixture generates synthetic Matroska byte streams entirely in Go.
// No external media files are referenced; every generator is deterministic
// for a given seed so manifests are reproducible.
package fixture

import (
	"bytes"
	"encoding/binary"
	"math/rand"
)

// Element IDs (mirrors internal/matroska constants).
const (
	IDEBMLHeader    uint32 = 0x1A45DFA3
	IDSegment       uint32 = 0x18538067
	IDSegmentInfo   uint32 = 0x1549A966
	IDTimecodeScale uint32 = 0x2AD7B1
	IDTracks        uint32 = 0x1654AE6B
	IDTrackEntry    uint32 = 0xAE
	IDTrackNumber   uint32 = 0xD7
	IDTrackType     uint32 = 0x83
	IDDefaultDur    uint32 = 0x23E383
	IDCluster       uint32 = 0x1F43B675
	IDTimecode      uint32 = 0xE7
	IDSimpleBlock   uint32 = 0xA3
)

// Corruption selects one of the five mandated fixture classes.
type Corruption string

const (
	CorruptNone            Corruption = ""
	CorruptUnknownNesting  Corruption = "unknown-nesting"
	CorruptClusterTruncate Corruption = "cluster-truncate"
	CorruptMissingTrack    Corruption = "missing-track"
	CorruptWrapDrift       Corruption = "wrap-drift"
	CorruptVFRMisalign     Corruption = "vfr-misalign"
)

// Options controls one generated representation (one .mkv byte stream).
type Options struct {
	Seed          int64
	Tracks        int  // number of tracks (1=video only, 2=video+audio)
	Clusters      int  // number of clusters
	BlocksPerClus int  // simple blocks per cluster
	UnknownLen    bool // emit unknown-length Segment/Cluster
	Wrap32        bool // drive timecodes across the 32-bit boundary
	DriftPPM      float64
	VFR           bool // variable frame intervals
	// ClusterStepMS is the timecode delta between successive clusters.
	// Zero defaults to 1000 ms so one cluster maps to one ~1 s segment.
	ClusterStepMS uint64
	Corrupt       Corruption
}

// Buffer accumulates EBML bytes with helpers.
type Buffer struct{ bytes.Buffer }

// VInt writes a data VINT of the given width (1..8).
func (b *Buffer) VInt(value uint64, width int) {
	out := make([]byte, width)
	v := value
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	out[0] |= byte(1) << uint(8-width)
	b.Write(out)
}

// UnknownSize writes the reserved 8-byte EBML unknown-length marker.
func (b *Buffer) UnknownSize() {
	b.Write([]byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
}

// ID writes an element ID at its natural width (1..4).
func (b *Buffer) ID(id uint32) {
	w := 4
	for w > 1 && id < uint32(1)<<uint(7*w-1) {
		w--
	}
	out := make([]byte, w)
	v := id
	for i := w - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	b.Write(out)
}

// Size writes a size VINT, or the 8-byte unknown-length marker when width<0.
func (b *Buffer) Size(size uint64) {
	for w := 1; w <= 8; w++ {
		limit := uint64(1) << uint(7*w)
		if size < limit-1 {
			b.VInt(size+limit, w)
			return
		}
	}
	panic("size too large for VINT")
}

// Element writes id + size + body.
func (b *Buffer) Element(id uint32, body []byte) {
	b.ID(id)
	b.Size(uint64(len(body)))
	b.Write(body)
}

// Uint encodes a big-endian uint payload at the given fixed width.
func Uint(v uint64, width int) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out[8-width:]
}

func simpleBlock(track uint64, rel int16, keyframe bool, payloadLen int) []byte {
	var body Buffer
	// Track VINT: small numbers fit one byte.
	body.VInt(track, 1)
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(rel))
	body.Write(tc[:])
	flags := byte(0)
	if keyframe {
		flags = 0x80
	}
	body.WriteByte(flags)
	body.Write(bytes.Repeat([]byte{0xA5}, payloadLen))
	return body.Bytes()
}

func trackEntry(number, trackType uint64, defaultDur uint64) []byte {
	var e Buffer
	e.Element(IDTrackNumber, Uint(number, 1))
	e.Element(IDTrackType, Uint(trackType, 1))
	if defaultDur > 0 {
		e.Element(IDDefaultDur, Uint(defaultDur, 4))
	}
	return e.Bytes()
}

// Generate builds one synthetic Matroska stream.
func Generate(opt Options) []byte {
	doc, _ := GenerateWithLayout(opt)
	return doc
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sizeWidth returns the VINT width needed to encode a payload of size bytes.
func sizeWidth(size int) int {
	for w := 1; w <= 8; w++ {
		if uint64(size) < (uint64(1)<<uint(7*w))-1 {
			return w
		}
	}
	panic("size too large for VINT")
}

// GenerateWithLayout additionally returns generation-time structural offsets.
func GenerateWithLayout(opt Options) ([]byte, Layout) {
	rng := rand.New(rand.NewSource(opt.Seed))
	var out Buffer
	var lay Layout

	// EBML header (fixed small body).
	out.Element(IDEBMLHeader, []byte{0x42, 0x82, 0x88, 0x6D, 0x61, 0x74, 0x72, 0x6F, 0x73, 0x6B, 0x61})

	var seg bytes.Buffer
	sb := &Buffer{Buffer: seg}

	// SegmentInfo with default 1 ms timecode scale.
	sb.Element(IDSegmentInfo, func() []byte {
		var i Buffer
		i.Element(IDTimecodeScale, Uint(1_000_000, 4))
		return i.Bytes()
	}())

	// Tracks.
	var tracks Buffer
	videoEntry := trackEntry(1, 1, 40_000_000) // 25 fps default duration
	tracks.Element(IDTrackEntry, videoEntry)
	if opt.Tracks >= 2 {
		tracks.Element(IDTrackEntry, trackEntry(2, 2, 20_000_000))
	}
	sb.Element(IDTracks, tracks.Bytes())

	unknownSeg := opt.UnknownLen || opt.Corrupt == CorruptUnknownNesting
	// Timecode progression, optionally wrapping the 32-bit boundary.
	clusterStep := opt.ClusterStepMS
	if clusterStep == 0 {
		clusterStep = 1000
	}
	tc := uint64(0)
	if opt.Wrap32 {
		tc = uint64(1)<<32 - uint64(opt.Clusters)*clusterStep
	}
	frameStep := clusterStep / uint64(maxi(opt.BlocksPerClus, 1))
	if frameStep == 0 {
		frameStep = clusterStep
	}
	type blockMark struct {
		clusterIdx, blockIdx int
		trackRel             int64 // offset relative to cluster element start
		payloadRel           int64 // -1 unless this is the truncation anchor
	}
	var marks []blockMark
	var clusterInner []int64
	var tcRel []int64 // timecode value offset relative to each cluster start
	var payloadInner int64 = -1
	for c := 0; c < opt.Clusters; c++ {
		var cluster bytes.Buffer
		cb := &Buffer{Buffer: cluster}
		cb.Element(IDTimecode, Uint(tc, 4)) // value follows ID(1)+size(1)
		tcRel = append(tcRel, 2)
		for k := 0; k < opt.BlocksPerClus; k++ {
			rel := int16(0)
			keyframe := k == 0
			payloadLen := 16 + rng.Intn(48)
			body := simpleBlock(1, rel, keyframe, payloadLen)
			trackRel := int64(cb.Len()) + 1 + int64(sizeWidth(len(body))) // skip block ID+size
			var pRel int64 = -1
			if c == 1 && k == 0 {
				pRel = trackRel + 4
			}
			cb.Element(IDSimpleBlock, body)
			marks = append(marks, blockMark{c, k, trackRel, pRel})
			if opt.Tracks >= 2 {
				bodyA := simpleBlock(2, 0, false, payloadLen/2)
				trackRelA := int64(cb.Len()) + 1 + int64(sizeWidth(len(bodyA)))
				cb.Element(IDSimpleBlock, bodyA)
				marks = append(marks, blockMark{c, k, trackRelA, -1})
			}
			// VFR: jitter the frame interval while keeping it positive.
			step := frameStep
			if opt.VFR && k%2 == 0 {
				step += 17
			}
			if opt.DriftPPM != 0 {
				step = uint64(float64(step) * (1 + opt.DriftPPM/1e6))
				if step == 0 {
					step = frameStep
				}
			}
			tc += step
		}
		clusterBody := cb.Bytes()
		clusterStart := sb.Len()
		clusterInner = append(clusterInner, int64(clusterStart))
		writeContainer(sb, IDCluster, clusterBody, unknownSeg)
	}

	// Assemble the segment (unknown-length variant for the nesting fixture).
	segmentBody := sb.Bytes()
	if unknownSeg {
		out.ID(IDSegment)
		out.UnknownSize() // unknown length
		lay.SegmentDataOff = int64(out.Len())
		out.Write(segmentBody)
	} else {
		out.Element(IDSegment, segmentBody)
		lay.SegmentDataOff = int64(out.Len() - len(segmentBody))
	}
	// Convert segment-relative offsets into absolute file offsets.
	clusterIDWidth := func() int {
		w := 4
		for w > 1 && IDCluster < uint32(1)<<uint(7*w-1) {
			w--
		}
		return w
	}()
	clusterHeader := func(ci int) int64 {
		if unknownSeg {
			return int64(clusterIDWidth) + 8 // ID + unknown size marker
		}
		// Element header = cluster ID + size VINT. The body length follows
		// from the span to the next cluster start (or the segment body end).
		start := clusterInner[ci]
		var nextStart int64 = int64(len(segmentBody))
		if ci+1 < len(clusterInner) {
			nextStart = clusterInner[ci+1]
		}
		span := int(nextStart - start)
		for w := 1; w <= 8; w++ {
			body := span - clusterIDWidth - w
			if body >= 0 && sizeWidth(body) == w {
				return int64(clusterIDWidth + w)
			}
		}
		return int64(clusterIDWidth + 1)
	}
	for _, m := range marks {
		abs := lay.SegmentDataOff + clusterInner[m.clusterIdx] + clusterHeader(m.clusterIdx) + m.trackRel
		lay.BlockTrackByteOff = append(lay.BlockTrackByteOff, abs)
		if m.payloadRel >= 0 {
			payloadInner = lay.SegmentDataOff + clusterInner[m.clusterIdx] + clusterHeader(m.clusterIdx) + m.payloadRel
		}
	}
	for i, inner := range clusterInner {
		lay.ClusterOff = append(lay.ClusterOff, lay.SegmentDataOff+inner)
		lay.ClusterTimecodeOff = append(lay.ClusterTimecodeOff,
			lay.SegmentDataOff+inner+clusterHeader(i)+tcRel[i])
	}
	if payloadInner >= 0 {
		lay.FirstBlockPayloadOff = payloadInner
	}

	doc := out.Bytes()
	return applyCorruption(doc, opt, lay), lay
}

// writeContainer appends a cluster either bounded or unknown-length.
func writeContainer(sb *Buffer, id uint32, body []byte, unknown bool) {
	if unknown {
		sb.ID(id)
		sb.UnknownSize()
		sb.Write(body)
		return
	}
	sb.Element(id, body)
}
