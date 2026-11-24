// Package fixture generates deterministic, self-contained Matroska/WebM
// samples and controlled corruptions. No external media files are referenced:
// payloads are produced by a seeded PRNG so the same seed always yields the
// same bytes and the same SHA-256 digests.
package fixture

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"

	"mediapack/internal/ebml"
)

// CorruptKind selects the controlled damage applied to an otherwise valid
// sample.
type CorruptKind uint8

const (
	CorruptNone                CorruptKind = iota
	CorruptTruncate                        // cut inside the final block payload
	CorruptBlockOversize                   // a block declares length beyond its container
	CorruptTimestampRegression             // a later cluster moves backwards in time
)

// Spec describes the valid sample before corruption.
type Spec struct {
	Seed           int64
	Codecs         []string // Matroska CodecIDs, e.g. V_VP9, A_OPUS
	Clusters       int
	FramesPerTrack int // blocks emitted per track inside each cluster
	PayloadMin     int
	PayloadMax     int
	UnknownLength  bool // emit unknown-size Segment and Cluster headers
}

// BlockInterval is the recorded [start,end) of one block's codec payload.
type BlockInterval struct {
	Track       uint32
	Start       int64
	End         int64
	AbsTimecode int64
}

// Detail records structural facts and, for corrupt samples, the exact byte
// offset of the first damaged location.
type Detail struct {
	Codecs        []string
	Tracks        []uint32
	ClusterCount  int
	BlockCount    int
	CorruptKind   CorruptKind
	CorruptOffset int64 // size-VINT anchor (truncation: exact cut byte)
	// CorruptEnd, for an oversize block, is its declared end: the scanner
	// reports a fault offset anywhere within [CorruptOffset, CorruptEnd].
	CorruptEnd int64
	Intervals  []BlockInterval
}

// Matroska element IDs used by the generator.
const (
	idDocType        uint32 = 0x4282
	idTimestampScale uint32 = 0x2AD7B1
	idTrackEntry     uint32 = 0xAE
	idTrackNumber    uint32 = 0xD7
	idTrackUID       uint32 = 0x73C5
	idTrackType      uint32 = 0x83
	idCodecID        uint32 = 0x86
	idVideo          uint32 = 0xE0
	idPixelWidth     uint32 = 0xB0
	idPixelHeight    uint32 = 0xBA
	idAudio          uint32 = 0xE1
	idSamplingFreq   uint32 = 0xB5
	idChannels       uint32 = 0x9F
)

// Build produces the sample described by spec, then applies corr. Exact block
// intervals are recovered by re-scanning the produced bytes with the offset
// aware EBML scanner, so fixture and parser can never disagree about ranges.
func Build(spec Spec, corr CorruptKind) ([]byte, Detail) {
	if spec.PayloadMin <= 0 {
		spec.PayloadMin = 16
	}
	if spec.PayloadMax < spec.PayloadMin {
		spec.PayloadMax = spec.PayloadMin
	}
	b := &builder{rng: rand.New(rand.NewSource(spec.Seed)), spec: spec}
	detail := Detail{CorruptKind: corr, CorruptOffset: -1, CorruptEnd: -1}

	header := b.ebmlHeader()
	segBody, oversizeOff, oversizeSpan := b.segmentBody(corr)
	var out []byte
	out = append(out, header...)
	if spec.UnknownLength {
		out = append(out, idBytes(ebml.IDSegment)...)
		out = append(out, 0xFF)
		out = append(out, segBody...)
	} else {
		// Fixed 8-byte size VINT: an injected oversize block changes the
		// segment payload length, and a fixed-width header keeps all earlier
		// byte offsets identical between the valid and corrupt samples.
		out = append(out, idBytes(ebml.IDSegment)...)
		out = append(out, fixedSize(uint64(len(segBody)))...)
		out = append(out, segBody...)
	}
	if oversizeOff >= 0 {
		// Translate the segment-relative size-VINT offset to absolute and
		// derive the declared end of the illegal block.
		detail.CorruptOffset = int64(len(header)) + oversizeOff
		detail.CorruptEnd = detail.CorruptOffset + oversizeSpan
	}
	if corr == CorruptTruncate {
		intervals, _ := ScanIntervals(out)
		last := intervals[len(intervals)-1]
		detail.CorruptOffset = last.Start + (last.End-last.Start)/2
		out = out[:detail.CorruptOffset]
	}
	intervals, _ := ScanIntervals(out)
	detail.Intervals = intervals
	detail.Codecs = spec.Codecs
	detail.Tracks = b.trackNumbers()
	detail.ClusterCount = spec.Clusters
	detail.BlockCount = len(intervals)
	return out, detail
}

// ScanIntervals re-parses a sample and returns every reachable block's codec
// payload interval in stream order. A non-nil error (e.g. truncation) means
// intervals before the fault are still returned.
func ScanIntervals(raw []byte) ([]BlockInterval, error) {
	seg, err := ebml.OpenSegment(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var out []BlockInterval
	scanErr := seg.ScanClusters(func(c *ebml.Cluster) error {
		for _, blk := range c.Blocks {
			out = append(out, BlockInterval{
				Track:       blk.Track,
				Start:       blk.PayloadStart,
				End:         blk.PayloadEnd,
				AbsTimecode: blk.AbsTimecode(c.Timestamp),
			})
		}
		return nil
	})
	return out, scanErr
}

type builder struct {
	rng  *rand.Rand
	spec Spec
}

func (b *builder) trackNumbers() []uint32 {
	nums := make([]uint32, len(b.spec.Codecs))
	for i := range b.spec.Codecs {
		nums[i] = uint32(i + 1)
	}
	return nums
}

func (b *builder) ebmlHeader() []byte {
	var body []byte
	body = append(body, uintElement(0x4286, 1)...) // EBMLVersion
	body = append(body, uintElement(0x42F7, 1)...) // EBMLReadVersion
	body = append(body, uintElement(0x42F2, 4)...) // EBMLMaxIDLength
	body = append(body, uintElement(0x42F3, 8)...) // EBMLMaxSizeLength
	body = append(body, strElement(idDocType, "webm")...)
	body = append(body, uintElement(0x4287, 2)...) // DocTypeVersion
	body = append(body, uintElement(0x4285, 2)...) // DocTypeReadVersion
	return wrap(ebml.IDEBMLHeader, body)
}

// segmentBody returns the segment payload, the segment-relative offset of an
// oversize block's size VINT (-1 when absent) and the byte span from that VINT
// to the block's declared end.
func (b *builder) segmentBody(corr CorruptKind) ([]byte, int64, int64) {
	var seg []byte
	info := uintElement(idTimestampScale, 1_000_000)
	seg = append(seg, wrap(ebml.IDInfo, info)...)
	var tracks []byte
	for i, codec := range b.spec.Codecs {
		num := uint64(i + 1)
		entry := uintElement(idTrackNumber, num)
		entry = append(entry, uintElement(idTrackUID, 1000+num)...)
		entry = append(entry, uintElement(idTrackType, trackType(codec))...)
		entry = append(entry, strElement(idCodecID, codec)...)
		entry = append(entry, mediaMaster(codec)...)
		tracks = append(tracks, wrap(idTrackEntry, entry)...)
	}
	seg = append(seg, wrap(ebml.IDTracks, tracks)...)

	oversize := int64(-1)
	span := int64(0)
	baseTS := int64(0)
	for c := 0; c < b.spec.Clusters; c++ {
		ts := baseTS
		if corr == CorruptTimestampRegression && c > 0 && c == b.spec.Clusters-1 {
			// Move the final cluster far enough back that its blocks precede
			// the previous cluster's blocks on every track.
			ts = baseTS - 150
		}
		cluster, off, sp := b.cluster(c, ts, corr)
		if off >= 0 {
			oversize = int64(len(seg)) + off
			span = sp
		}
		seg = append(seg, cluster...)
		baseTS += 100
	}
	return seg, oversize, span
}

func (b *builder) cluster(clusterIdx int, ts int64, corr CorruptKind) ([]byte, int64, int64) {
	var body []byte
	oversizeOff := int64(-1)
	oversizeSpan := int64(0)
	body = append(body, uintElement(ebml.IDTimestamp, uint64(ts))...)
	for ti, codec := range b.spec.Codecs {
		trackNum := uint32(ti + 1)
		for f := 0; f < b.spec.FramesPerTrack; f++ {
			size := b.spec.PayloadMin +
				b.rng.Intn(b.spec.PayloadMax-b.spec.PayloadMin+1)
			payload := make([]byte, size)
			b.rng.Read(payload)
			payload[0] = codecMarker(codec)
			rel := int16(f * 5)
			block := encodeBlock(trackNum, rel, true, payload)
			inject := corr == CorruptBlockOversize &&
				clusterIdx == b.spec.Clusters-1 && ti == len(b.spec.Codecs)-1 &&
				f == b.spec.FramesPerTrack-1
			if inject {
				declaredLen := uint64(len(block) + 20)
				sizeBytes := encodeSize(declaredLen)
				// Anchor at the block's size VINT; span reaches the declared
				// end, which is where the scanner first proves the overrun.
				oversizeOff = int64(len(body)) + 4
				oversizeSpan = int64(len(sizeBytes)) + int64(declaredLen)
				body = append(body, idBytes(ebml.IDSimpleBlock)...)
				body = append(body, sizeBytes...)
				body = append(body, block...)
			} else {
				body = append(body, wrap(ebml.IDSimpleBlock, block)...)
			}
		}
	}
	if b.spec.UnknownLength {
		return append(append(idBytes(ebml.IDCluster), 0xFF), body...), oversizeOff, oversizeSpan
	}
	// Fixed-width size header so an injected oversize block does not shift
	// the size VINT width or corrupt earlier cluster offsets.
	out := append(idBytes(ebml.IDCluster), fixedSize(uint64(len(body)))...)
	return append(out, body...), oversizeOff, oversizeSpan
}

// --- low-level encoders -------------------------------------------------

func idBytes(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// encodeSize emits a minimal-width EBML size VINT (the all-ones value is
// reserved and therefore never produced here).
func encodeSize(v uint64) []byte {
	for width := 1; width <= 8; width++ {
		if v < (uint64(1)<<(7*width))-1 {
			out := make([]byte, width)
			val := v | (uint64(1) << (7 * width))
			for i := width - 1; i >= 0; i-- {
				out[i] = byte(val >> (8 * (width - 1 - i)))
			}
			return out
		}
	}
	panic("EBML size does not fit in 8 bytes")
}

// fixedSize emits an 8-byte EBML size VINT carrying v (legal per the EBML
// spec when EBMLMaxSizeLength >= 8). Used for container masters whose payload
// length may change after a controlled corruption is injected.
func fixedSize(v uint64) []byte {
	out := make([]byte, 8)
	out[0] = 0x01 // length marker for an 8-byte VINT
	for i := 7; i > 0; i-- {
		out[i] = byte(v >> (8 * (7 - i)))
	}
	return out
}

func wrap(id uint32, payload []byte) []byte {
	return append(append(idBytes(id), encodeSize(uint64(len(payload)))...), payload...)
}

func uintElement(id uint32, v uint64) []byte {
	var raw []byte
	if v == 0 {
		raw = []byte{0}
	} else {
		for x := v; x > 0; x >>= 8 {
			raw = append([]byte{byte(x)}, raw...)
		}
	}
	return wrap(id, raw)
}

func strElement(id uint32, s string) []byte { return wrap(id, []byte(s)) }

func floatElement(id uint32, f float64) []byte {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, math.Float64bits(f))
	return wrap(id, raw)
}

func encodeBlock(track uint32, rel int16, keyframe bool, payload []byte) []byte {
	flags := byte(0)
	if keyframe {
		flags |= 0x80
	}
	head := []byte{0x80 | byte(track), byte(rel >> 8), byte(rel), flags}
	return append(head, payload...)
}

func trackType(codec string) uint64 {
	switch {
	case len(codec) > 0 && codec[0] == 'V':
		return 1
	case len(codec) > 0 && codec[0] == 'A':
		return 2
	default:
		return 17 // Matroska subtitle track type
	}
}

func codecMarker(codec string) byte {
	switch codec {
	case "V_VP8":
		return 0x10
	case "V_VP9":
		return 0x9F
	case "A_OPUS":
		return 0x4F // 'O'
	case "A_VORBIS":
		return 0x01
	default:
		return 0x57 // 'W' WebVTT
	}
}

func mediaMaster(codec string) []byte {
	switch trackType(codec) {
	case 1:
		v := append(uintElement(idPixelWidth, 320), uintElement(idPixelHeight, 240)...)
		return wrap(idVideo, v)
	case 2:
		a := append(floatElement(idSamplingFreq, 48000), uintElement(idChannels, 2)...)
		return wrap(idAudio, a)
	default:
		return nil
	}
}
