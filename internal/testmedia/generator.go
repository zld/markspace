// Package testmedia generates deterministic, in-memory Matroska/WebM fixtures
// and synthetic RTP streams. No external files or network resources are used;
// every byte derives from the seed so repeated runs are identical.
package testmedia

import (
	"math/rand"

	"mediasync/internal/ebml"
)

// Track kinds understood by the generator.
const (
	KindVideo    = "video"
	KindAudio    = "audio"
	KindSubtitle = "subtitle"
)

type trackSpec struct {
	kind     string
	number   uint32
	codecID  string
	tickStep int64 // milliseconds between blocks
	payLoad  int   // payload bytes per block
}

// Spec configures one generated container.
type Spec struct {
	Seed             int64
	Tracks           []string // subset of video/audio/subtitle
	Clusters         int
	BlocksPerCluster int
	WrapAt           int64 // >0 wraps absolute millisecond ticks
}

// BlockTruth records what the generator intended for one block, letting tests
// verify demux and remux independently of the parser under test.
type BlockTruth struct {
	Track   uint32
	Seq     uint64
	AbsTick int64
	Payload []byte
}

// Truth is the full generation manifest.
type Truth struct {
	ScaleNS int64
	Blocks  []BlockTruth
	Tracks  []TrackTruth
}

// TrackTruth describes one generated track header.
type TrackTruth struct {
	Number  uint32
	Kind    string
	CodecID string
}

func trackFor(kind string) trackSpec {
	switch kind {
	case KindVideo:
		return trackSpec{kind: kind, number: 1, codecID: "V_VP9", tickStep: 33, payLoad: 48}
	case KindAudio:
		return trackSpec{kind: kind, number: 2, codecID: "A_OPUS", tickStep: 20, payLoad: 32}
	default:
		return trackSpec{kind: kind, number: 3, codecID: "S_TEXT/WEBVTT", tickStep: 250, payLoad: 24}
	}
}

// BuildWebM serializes a complete, known-length WebM byte slice plus the
// truth manifest. Clusters are known-length; pass UnknownSegment to flip the
// Segment to unknown-length encoding.
func BuildWebM(spec Spec) ([]byte, Truth) {
	rng := rand.New(rand.NewSource(spec.Seed))
	specs := make([]trackSpec, 0, len(spec.Tracks))
	for _, k := range spec.Tracks {
		specs = append(specs, trackFor(k))
	}
	truth := Truth{ScaleNS: 1_000_000}
	for _, ts := range specs {
		truth.Tracks = append(truth.Tracks, TrackTruth{
			Number: ts.number, Kind: ts.kind, CodecID: ts.codecID,
		})
	}

	out := buildEBMLHeader()
	segBody := buildInfo(truth.ScaleNS)
	segBody = append(segBody, buildTracks(specs)...)

	// Build blocks in absolute-time order, then cut them into clusters.
	var planned []BlockTruth
	counters := map[uint32]uint64{}
	tick := map[uint32]int64{}
	for c := 0; c < spec.Clusters; c++ {
		for b := 0; b < spec.BlocksPerCluster; b++ {
			for _, ts := range specs {
				counters[ts.number]++
				abs := tick[ts.number]
				tick[ts.number] += ts.tickStep
				payload := make([]byte, ts.payLoad)
				rng.Read(payload)
				planned = append(planned, BlockTruth{
					Track: ts.number, Seq: counters[ts.number],
					AbsTick: abs, Payload: payload,
				})
			}
		}
	}
	truth.Blocks = planned

	// Group blocks into clusters by generation rounds; each round becomes one
	// cluster starting at the minimum absolute tick inside it.
	perCluster := spec.BlocksPerCluster * len(specs)
	for c := 0; c < spec.Clusters; c++ {
		lo := c * perCluster
		hi := lo + perCluster
		if hi > len(planned) {
			hi = len(planned)
		}
		group := planned[lo:hi]
		base := group[0].AbsTick
		for _, g := range group {
			if g.AbsTick < base {
				base = g.AbsTick
			}
		}
		clusterTC := base
		if spec.WrapAt > 0 {
			clusterTC = base % spec.WrapAt
		}
		cb := &ebml.MasterBuilder{}
		cb.Uint(ebml.IDTimecode, uint64(clusterTC))
		for _, blk := range group {
			rel := blk.AbsTick - base
			if spec.WrapAt > 0 {
				rel = wrapRel(blk.AbsTick, base, spec.WrapAt)
			}
			cb.Write(encodeSimpleBlock(blk.Track, rel, blk.Payload, blk.Seq == 1))
		}
		segBody = append(segBody, ebml.Build(ebml.IDCluster, cb)...)
	}
	out = append(out, ebml.EncodeElement(ebml.IDSegment, segBody)...)
	return out, truth
}

// wrapRel computes a cluster-relative timecode across a wrap boundary,
// keeping the result inside signed 16-bit range.
func wrapRel(abs, base, wrap int64) int64 {
	rel := abs - base
	if rel < -wrap/2 {
		rel += wrap
	}
	return rel
}

func encodeSimpleBlock(track uint32, relMS int64, payload []byte, keyframe bool) []byte {
	header := []byte{0x80 | byte(track)} // 1-byte track VINT (tracks 1..3)
	header = append(header, byte(relMS>>8), byte(relMS))
	var flags byte
	if keyframe {
		flags = 0x80
	}
	body := append(append(header, flags), payload...)
	return ebml.EncodeElement(ebml.IDSimpleBlock, body)
}

func buildEBMLHeader() []byte {
	hb := &ebml.MasterBuilder{}
	hb.Uint(0x4286, 1)        // EBMLVersion
	hb.Uint(0x42F7, 1)        // EBMLReadVersion
	hb.Uint(0x42F2, 4)        // EBMLMaxIDLength
	hb.Uint(0x42F3, 8)        // EBMLMaxSizeLength
	hb.String(0x4282, "webm") // DocType
	hb.Uint(0x4287, 2)        // DocTypeVersion
	hb.Uint(0x4285, 2)        // DocTypeReadVersion
	return ebml.Build(ebml.IDEBMLExtra, hb)
}

func buildInfo(scaleNS int64) []byte {
	ib := &ebml.MasterBuilder{}
	ib.Uint(ebml.IDTimecodeScale, uint64(scaleNS))
	ib.String(ebml.IDMuxingApp, "mediasync-testmedia")
	ib.String(ebml.IDWritingApp, "mediasync")
	return ebml.Build(ebml.IDInfo, ib)
}

func buildTracks(specs []trackSpec) []byte {
	tb := &ebml.MasterBuilder{}
	for _, ts := range specs {
		entry := &ebml.MasterBuilder{}
		entry.Uint(ebml.IDTrackNumber, uint64(ts.number))
		entry.Uint(ebml.IDTrackUID, uint64(ts.number)*1000)
		switch ts.kind {
		case KindVideo:
			entry.Uint(ebml.IDTrackType, 1)
		case KindAudio:
			entry.Uint(ebml.IDTrackType, 2)
		default:
			entry.Uint(ebml.IDTrackType, 17)
		}
		entry.String(ebml.IDCodecID, ts.codecID)
		entry.Uint(ebml.IDFlagLacing, 0)
		tb.Master(ebml.IDTrackEntry, entry)
	}
	return ebml.Build(ebml.IDTracks, tb)
}
