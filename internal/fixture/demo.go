package fixture

import (
	"fmt"
	"math/rand"
	"strings"

	"mediamux/manifest"
)

// Demo is a self-contained set of manifests, segments and transport packets.
type Demo struct {
	Seed        int64
	HLSRaw      string
	DASHRaw     string
	HLSDoc      *manifest.Document
	DASHDoc     *manifest.Document
	Segments    [][]byte
	SegmentIDs  []string
	SegmentSecs []string
	TargetBPS   int64
	RTP         [][][]byte
	// Faulty variants, derived deterministically from the clean segments.
	Truncated      [][]byte
	BadBoxLength   [][]byte
	BadManifestRaw string
	DuplicateRTP   [][][]byte
	LostRTP        [][][]byte
}

// SegmentCount is the number of media segments each demo run generates.
const SegmentCount = 4

// BuildDemo generates the full demo corpus for seed. The same seed always
// yields byte-identical manifests, segments and packets.
func BuildDemo(seed int64) (*Demo, error) {
	rng := rand.New(rand.NewSource(seed))
	d := &Demo{Seed: seed}

	// Each segment is 2.0017s of 90kHz media: 50 samples of 3603 ticks.
	// At 1.2 Mbps that is ~300 KB per segment, so a queue depth of 2 plus the
	// segment under analysis stays under the demo's 1 MiB ceiling.
	const sampleTicks = 3603
	const samples = 50
	const segmentSeconds = 2.0016666666666667
	d.TargetBPS = 1_200_000
	targetBytes := int(float64(d.TargetBPS) / 8 * segmentSeconds)

	// Vary each segment's size by +0.1% to +1.5% over the nominal target so
	// the sliding window has real variation while the average stays in the
	// required 0-5% band above the declared rate for every seed.
	for i := 0; i < SegmentCount; i++ {
		jitter := 1.001 + rng.Float64()*0.014
		bytes := int(float64(targetBytes) * jitter)
		seg := BuildSegment(SegmentSpec{
			Samples: samples, SampleTicks: sampleTicks, TargetBytes: bytes,
			KeyframeEvery: 25, Seed: seed*1000 + int64(i),
		})
		d.Segments = append(d.Segments, seg)
		d.SegmentIDs = append(d.SegmentIDs, fmt.Sprintf("seg-%d.m4s", i))
		d.SegmentSecs = append(d.SegmentSecs, "2.002")
	}

	d.HLSRaw = strings.Replace(HLSManifest("7", d.SegmentSecs, d.SegmentIDs),
		"#EXT-X-MEDIA-SEQUENCE:0\n",
		fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-BITRATE:%d\n", d.TargetBPS/1000), 1)
	doc, err := manifest.ParseHLS(d.HLSRaw)
	if err != nil {
		return nil, err
	}
	d.HLSDoc = doc

	// The DASH representation mirrors the same timeline at its own timescale.
	// Four periods of 180180 ticks (2.002s each) cover the same timeline.
	d.DASHRaw = DASHManifest("PT8.008S", 90000, 180180, d.TargetBPS)
	dashDoc, err := manifest.ParseDASH(d.DASHRaw)
	if err != nil {
		return nil, err
	}
	d.DASHDoc = dashDoc

	// Faulty media: the first segment is truncated mid-index, the second has
	// an impossible box length, both at deterministic offsets.
	d.Truncated = cloneAll(d.Segments)
	d.Truncated[0] = Truncate(d.Segments[0], len(d.Segments[0])*45/100)
	d.BadBoxLength = cloneAll(d.Segments)
	moovAt := strings.Index(string(d.BadBoxLength[1]), "moov")
	if moovAt >= 4 {
		d.BadBoxLength[1] = CorruptBoxLength(d.Segments[1], moovAt-4)
	}

	// A manifest whose declared durations no longer match the media the
	// fragments actually carry: 5.000s claimed, 6.005s delivered.
	badDurations := []string{"5.000", "5.000", "5.000", "5.000"}
	d.BadManifestRaw = HLSManifest("6", badDurations, d.SegmentIDs)

	// Transport evidence: one stream with duplicates, one with sequence gaps.
	d.RTP = make([][][]byte, SegmentCount)
	d.DuplicateRTP = make([][][]byte, SegmentCount)
	d.LostRTP = make([][][]byte, SegmentCount)
	for i := 0; i < SegmentCount; i++ {
		clean, err := BuildRTPPackets(120, uint16(i*1000), uint32(i)*180150, 1100, nil, nil)
		if err != nil {
			return nil, err
		}
		dups := map[uint16]bool{uint16(i*1000 + 40): true, uint16(i*1000 + 41): true}
		dup, err := BuildRTPPackets(120, uint16(i*1000), uint32(i)*180150, 1100, nil, dups)
		if err != nil {
			return nil, err
		}
		drops := map[uint16]bool{
			uint16(i*1000 + 90): true, uint16(i*1000 + 91): true, uint16(i*1000 + 92): true,
		}
		loss, err := BuildRTPPackets(120, uint16(i*1000), uint32(i)*180150, 1100, drops, nil)
		if err != nil {
			return nil, err
		}
		d.RTP[i] = clean
		d.DuplicateRTP[i] = dup
		d.LostRTP[i] = loss
	}
	return d, nil
}

// BuildRTPPackets is the RTP fixture builder used by the demo.
func BuildRTPPackets(count int, seqStart uint16, tsStart uint32, payloadLen int, drop, dup map[uint16]bool) ([][]byte, error) {
	return buildRTP(count, seqStart, tsStart, payloadLen, drop, dup)
}

func cloneAll(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = append([]byte(nil), in[i]...)
	}
	return out
}
