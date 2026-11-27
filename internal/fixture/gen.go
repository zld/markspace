// Package fixture deterministically generates the local sample inputs
// for the continuity pipeline: a Matroska/WebM document with
// interleaved audio/video blocks, a minimal MP4 init segment carrying
// backup-source colour metadata, and an RTP delivery exercising
// reorder, duplication, loss bursts, wraparound and late arrival.
package fixture

import (
	"encoding/binary"
	"math"
	"math/rand"

	pionrtp "github.com/pion/rtp"

	"continuity/internal/ebml"
)

// RTP stream parameters of the generated backup-source audio.
const (
	RTPPackets   = 24
	RTPBaseSeq   = 65530 // crosses the 16-bit wrap at packet 6
	RTPBaseTS    = 5000
	RTPClockRate = 44100
	RTPSamples   = 882 // 20 ms per packet
	RTPSSRC      = 0x11223344
)

// Inputs bundles the generated sample data.
type Inputs struct {
	Matroska []byte
	MP4Init  []byte
	RTP      [][]byte // marshaled packets in delivery order
}

// Generate builds the inputs for a seed. corrupt selects a fault
// variant: "truncated-block" cuts the Matroska data mid-block, and
// "unknown-length-boundary" emits an unknown-size Cluster terminated
// at its legal parent boundary.
func Generate(seed int64, corrupt string) Inputs {
	rng := rand.New(rand.NewSource(seed))
	segmentUnknown := corrupt == "truncated-block"
	clusterUnknown := corrupt == "unknown-length-boundary" || corrupt == "truncated-block"
	mk := buildMatroska(rng, segmentUnknown, clusterUnknown)
	if corrupt == "truncated-block" {
		mk = mk[:len(mk)-7] // cut inside the final SimpleBlock payload
	}
	return Inputs{
		Matroska: mk,
		MP4Init:  buildMP4Init(),
		RTP:      buildRTP(rng),
	}
}

// --- EBML construction helpers ---

func idb(id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		return []byte{byte(id >> 8), byte(id)}
	}
	return []byte{byte(id)}
}

func sizeBytes(n int) []byte {
	for l := 1; l <= 8; l++ {
		if uint64(n) < (uint64(1)<<(7*uint(l)))-1 {
			b := make([]byte, l)
			for i, v := l-1, n; i >= 0; i, v = i-1, v>>8 {
				b[i] = byte(v)
			}
			b[0] |= 1 << (8 - uint(l))
			return b
		}
	}
	panic("element too large")
}

func elem(id uint32, payload []byte) []byte {
	out := idb(id)
	out = append(out, sizeBytes(len(payload))...)
	return append(out, payload...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func uintElem(id uint32, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	n := 8
	for n > 1 && b[8-n] == 0 {
		n--
	}
	return elem(id, b[8-n:])
}

func floatElem(id uint32, f float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(f))
	return elem(id, b[:])
}

func strElem(id uint32, s string) []byte { return elem(id, []byte(s)) }

// --- Matroska document ---

func simpleBlock(rng *rand.Rand, track byte, tc int16, flags byte) []byte {
	payload := []byte{0x80 | track}
	var tcb [2]byte
	binary.BigEndian.PutUint16(tcb[:], uint16(tc))
	payload = append(payload, tcb[:]...)
	payload = append(payload, flags)
	var p [8]byte
	rng.Read(p[:])
	return elem(ebml.IDSimpleBlock, append(payload, p[:]...))
}

func buildMatroska(rng *rand.Rand, segmentUnknown, clusterUnknown bool) []byte {
	head := elem(ebml.IDEBML, concat(
		uintElem(ebml.IDEBMLVersion, 1), uintElem(ebml.IDEBMLReadVersion, 1),
		uintElem(ebml.IDEBMLMaxIDLength, 4), uintElem(ebml.IDEBMLMaxSizeLength, 8),
		strElem(ebml.IDDocType, "matroska"),
		uintElem(ebml.IDDocTypeVersion, 4), uintElem(ebml.IDDocTypeReadVersion, 2),
	))
	info := elem(ebml.IDInfo, concat(
		uintElem(ebml.IDTimecodeScale, 1000000), // 1 tick = 1 ms
		strElem(ebml.IDMuxingApp, "continuity-fixture"),
		strElem(ebml.IDWritingApp, "continuity-fixture"),
	))
	video := elem(ebml.IDTrackEntry, concat(
		uintElem(ebml.IDTrackNumber, 1), uintElem(ebml.IDTrackUID, 101),
		uintElem(ebml.IDTrackType, 1), strElem(ebml.IDCodecID, "V_UNCOMPRESSED"),
		elem(ebml.IDVideo, concat(
			uintElem(ebml.IDPixelWidth, 1280), uintElem(ebml.IDPixelHeight, 720),
			elem(ebml.IDColour, concat(
				uintElem(ebml.IDMatrixCoefficients, 1), // bt709
				uintElem(ebml.IDTransferCharacteristics, 1),
				uintElem(ebml.IDPrimaries, 1),
				uintElem(ebml.IDRange, 1),
			)),
		)),
	))
	audio := elem(ebml.IDTrackEntry, concat(
		uintElem(ebml.IDTrackNumber, 2), uintElem(ebml.IDTrackUID, 102),
		uintElem(ebml.IDTrackType, 2), strElem(ebml.IDCodecID, "A_PCM/INT/LIT"),
		elem(ebml.IDAudio, concat(
			floatElem(ebml.IDSamplingFrequency, 48000),
			uintElem(ebml.IDChannels, 2),
		)),
	))
	tracks := elem(ebml.IDTracks, concat(video, audio))

	// Interleaved blocks over 1.6 s: video every 40 ms, audio every
	// 20 ms offset by +5 ms.
	var blocks []byte
	for vi, ai := 0, 0; vi*40 <= 1600 || 5+ai*20 <= 1600; {
		if vt, at := vi*40, 5+ai*20; vt <= at {
			blocks = append(blocks, simpleBlock(rng, 1, int16(vt), 0x80)...)
			vi++
		} else {
			blocks = append(blocks, simpleBlock(rng, 2, int16(at), 0x00)...)
			ai++
		}
	}
	body := concat(uintElem(ebml.IDTimecode, 0), blocks)
	var cluster []byte
	if clusterUnknown {
		cluster = idb(ebml.IDCluster)
		cluster = append(cluster, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
		cluster = append(cluster, body...)
	} else {
		cluster = elem(ebml.IDCluster, body)
	}
	segmentBody := concat(info, tracks, cluster)
	var segment []byte
	if segmentUnknown {
		// Unknown-size Segment (common for live streams): terminates
		// at EOF, so a mid-block truncation is reported at the block.
		segment = idb(ebml.IDSegment)
		segment = append(segment, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
		segment = append(segment, segmentBody...)
	} else {
		segment = elem(ebml.IDSegment, segmentBody)
	}
	return concat(head, segment)
}

// --- MP4 init segment (backup-source colour metadata) ---

func mp4box(typ string, payload []byte) []byte {
	var b [8]byte
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	return append(b[:], payload...)
}

func buildMP4Init() []byte {
	ftyp := mp4box("ftyp", concat([]byte("isom"), []byte{0, 0, 2, 0}, []byte("isomiso6")))
	var c [7]byte
	binary.BigEndian.PutUint16(c[0:2], 1) // primaries: bt709
	binary.BigEndian.PutUint16(c[2:4], 1) // transfer: bt709
	binary.BigEndian.PutUint16(c[4:6], 5) // matrix: bt601 -> conflicts with container
	return concat(ftyp, mp4box("colr", append([]byte("nclx"), c[:]...)))
}

// --- RTP delivery ---

func buildRTP(rng *rand.Rand) [][]byte {
	pkts := make([][]byte, RTPPackets)
	for i := 0; i < RTPPackets; i++ {
		p := &pionrtp.Packet{
			Header: pionrtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(RTPBaseSeq + i),
				Timestamp:      RTPBaseTS + uint32(i)*RTPSamples,
				SSRC:           RTPSSRC,
			},
			Payload: make([]byte, 16),
		}
		rng.Read(p.Payload)
		b, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		pkts[i] = b
	}
	// Delivery order: reorder 3/4 and 15/16, duplicate 5 and 20, drop
	// 10 and 12 (burst loss with 11), deliver 11 late at the end.
	order := []int{0, 1, 2, 4, 3, 5, 5, 6, 7, 8, 9, 13, 14, 16, 15, 17, 18, 19, 20, 20, 21, 22, 23, 11}
	out := make([][]byte, 0, len(order))
	for _, i := range order {
		out = append(out, pkts[i])
	}
	return out
}
