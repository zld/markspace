// Package fixture generates deterministic sample media entirely in code. No
// external files are referenced. The scenario covers a clean track, a track
// with duplicates and gaps, a truncated-packet track, an illegal AU-header
// track, an MP4-backed AAC track and a colour-metadata conflict, plus the
// expected markers the acceptance run verifies against.
package fixture

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// Markers record the defects deliberately injected into a track.
type Markers struct {
	DuplicateOffsets []int    `json:"duplicate_offsets,omitempty"`
	GapSequences     []uint64 `json:"gap_sequences,omitempty"`
	TruncatedOffsets []int    `json:"truncated_offsets,omitempty"`
}

// Scenario is the generated input set and its expected defect markers.
type Scenario struct {
	Seed   int64                 `json:"seed"`
	Tracks []model.TrackInput    `json:"-"`
	Expect map[string]Markers    `json:"expect"`
	Order  []string              `json:"track_order"`
}

const (
	opusPT      = 111
	aacPT       = 97
	opus20msTOC = 0x08 // config 1 (20 ms SILK), code 0 (one frame)
)

// Generate builds a reproducible scenario from seed.
func Generate(seed int64) *Scenario {
	rng := rand.New(rand.NewSource(seed))
	sc := &Scenario{Seed: seed, Expect: map[string]Markers{}}

	sc.Tracks = append(sc.Tracks, cleanDialogue(rng))
	sc.Tracks = append(sc.Tracks, sfxWithDupAndGap(rng, sc.Expect))
	sc.Tracks = append(sc.Tracks, brokenOpus(rng, sc.Expect))
	sc.Tracks = append(sc.Tracks, aacRTPTruncated(rng, sc.Expect))
	sc.Tracks = append(sc.Tracks, aacMP4(rng))
	sc.Tracks = append(sc.Tracks, videoColorConflict())
	sc.Tracks = append(sc.Tracks, cycleTracks()...)

	for _, t := range sc.Tracks {
		sc.Order = append(sc.Order, t.Meta.TrackID)
	}
	return sc
}

func filler(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Intn(256))
	}
	return b
}

func rtpPacket(seq uint16, pt uint8, ssrc uint32, payload []byte) model.RawPacket {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: pt, SequenceNumber: seq, SSRC: ssrc,
			Timestamp: uint32(seq) * 960,
		},
		Payload: payload,
	}
	b, err := pkt.Marshal()
	if err != nil {
		panic(err) // fixture bug, not an input failure
	}
	return model.RawPacket{Offset: int(seq), RTP: b}
}

func cleanDialogue(rng *rand.Rand) model.TrackInput {
	const n = 8
	pkts := make([]model.RawPacket, n)
	for s := 0; s < n; s++ {
		payload := append([]byte{opus20msTOC}, filler(rng, 24)...)
		pkts[s] = rtpPacket(uint16(s), opusPT, 1001, payload)
		pkts[s].Offset = s
	}
	return model.TrackInput{
		Meta: model.TrackMeta{
			TrackID: "dialogue", Codec: model.CodecOpus,
			ClockRate: 48000, Channels: 2, SampleRate: 48000,
		},
		Packets: pkts,
	}
}

func sfxWithDupAndGap(rng *rand.Rand, expect map[string]Markers) model.TrackInput {
	// Sequences 0..6 with seq 2 duplicated and seq 5 missing.
	seqs := []uint16{0, 1, 2, 3, 4, 6, 2}
	mk := Markers{}
	pkts := make([]model.RawPacket, 0, len(seqs))
	for off, s := range seqs {
		payload := append([]byte{opus20msTOC}, filler(rng, 16)...)
		pk := rtpPacket(s, opusPT, 1002, payload)
		pk.Offset = off
		pkts = append(pkts, pk)
		if s == 2 && off != 2 {
			mk.DuplicateOffsets = append(mk.DuplicateOffsets, off)
		}
	}
	mk.GapSequences = append(mk.GapSequences, 5)
	expect["sfx"] = mk
	return model.TrackInput{
		Meta: model.TrackMeta{
			TrackID: "sfx", Codec: model.CodecOpus,
			ClockRate: 48000, Channels: 1, SampleRate: 48000,
		},
		Packets: pkts,
	}
}

func brokenOpus(rng *rand.Rand, expect map[string]Markers) model.TrackInput {
	const badOffset = 4
	mk := Markers{TruncatedOffsets: []int{badOffset}}
	expect["broken"] = mk
	pkts := make([]model.RawPacket, 0, 7)
	for off := 0; off < 7; off++ {
		var payload []byte
		if off == badOffset {
			payload = nil // valid RTP header, empty Opus payload -> located failure
		} else {
			payload = append([]byte{opus20msTOC}, filler(rng, 12)...)
		}
		pk := rtpPacket(uint16(off), opusPT, 1003, payload)
		pk.Offset = off
		pkts = append(pkts, pk)
	}
	return model.TrackInput{
		Meta: model.TrackMeta{
			TrackID: "broken", Codec: model.CodecOpus,
			ClockRate: 48000, Channels: 2, SampleRate: 48000,
		},
		Packets: pkts,
	}
}

func aacRTPTruncated(rng *rand.Rand, expect map[string]Markers) model.TrackInput {
	const badOffset = 3
	mk := Markers{TruncatedOffsets: []int{badOffset}}
	expect["aac-rtp"] = mk
	pkts := make([]model.RawPacket, 0, 6)
	for off := 0; off < 6; off++ {
		var payload []byte
		if off == badOffset {
			// Declare one 200-byte AU but supply only 3 bytes: illegal/truncated.
			word := uint16(200) << 3
			payload = []byte{0x00, 0x10, byte(word >> 8), byte(word), 1, 2, 3}
		} else {
			payload = buildAACPacket(rng, 40+off)
		}
		pk := rtpPacket(uint16(off), aacPT, 1004, payload)
		pk.Offset = off
		pkts = append(pkts, pk)
	}
	return model.TrackInput{
		Meta: model.TrackMeta{
			TrackID: "aac-rtp", Codec: model.CodecAAC,
			ClockRate: 48000, Channels: 6, SampleRate: 48000,
		},
		Packets: pkts,
	}
}

func buildAACPacket(rng *rand.Rand, auSize int) []byte {
	out := []byte{0x00, 0x10} // 16 bits of AU headers
	// AU header = 13-bit AU size in the high bits, 3-bit index delta (0).
	word := uint16(auSize) << 3
	out = append(out, byte(word>>8), byte(word))
	return append(out, filler(rng, auSize)...)
}

func aacMP4(rng *rand.Rand) model.TrackInput {
	const n = 5
	blob := BuildMinimalMP4(48000, n)
	pkts := make([]model.RawPacket, n)
	for s := 0; s < n; s++ {
		// Payload content is irrelevant: the MP4 sample table drives timing.
		pkts[s] = rtpPacket(uint16(s), aacPT, 1005, filler(rng, 8))
		pkts[s].Offset = s
	}
	return model.TrackInput{
		Meta: model.TrackMeta{
			TrackID: "aac-mp4", Codec: model.CodecAAC,
			ClockRate: 48000, Channels: 2, SampleRate: 48000,
		},
		Packets:   pkts,
		MP4Data:   blob,
		DependsOn: []string{"dialogue"},
	}
}

func videoColorConflict() model.TrackInput {
	return model.TrackInput{
		Meta: model.TrackMeta{
			TrackID: "video", Codec: model.CodecVideo, ClockRate: 90000,
			Color: &model.ColorMeta{
				ColorPrimaries: 1, TransferCharacteristics: 1,
				MatrixCoefficients: 1, FullRange: true,
			},
		},
	}
}

// cycleTracks returns two packetless video tracks that depend on each other.
// The graph must detect the cycle and deny both a processing configuration.
func cycleTracks() []model.TrackInput {
	meta := func(id string) model.TrackMeta {
		return model.TrackMeta{TrackID: id, Codec: model.CodecVideo, ClockRate: 90000}
	}
	return []model.TrackInput{
		{Meta: meta("cyc-a"), DependsOn: []string{"cyc-b"}},
		{Meta: meta("cyc-b"), DependsOn: []string{"cyc-a"}},
	}
}

// --- minimal, deterministic MP4 construction (ftyp + moov) -----------------

func box(typ string, payload []byte) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	return append(hdr, payload...)
}

func fullBox(typ string, version uint8, flags uint32, payload []byte) []byte {
	fb := make([]byte, 4)
	fb[0] = version
	fb[1] = byte(flags >> 16)
	fb[2] = byte(flags >> 8)
	fb[3] = byte(flags)
	return box(typ, append(fb, payload...))
}

// BuildMinimalMP4 writes ftyp + moov/trak/mdia with mdhd, stbl(stts,stsz) and
// a colr box. The parser keys on box types, so the nesting is sufficient for
// the sample-table and colour extraction exercised by the kernel.
func BuildMinimalMP4(timescale uint32, samples int) []byte {
	ftyp := box("ftyp", append([]byte("isom"), 0, 0, 0, 0))

	// mdhd v0 payload: creation(4) modification(4) timescale(4) duration(4)
	// then language(2) + pre_defined(2) = 20 bytes after the full-box header.
	mdhdPayload := make([]byte, 20)
	binary.BigEndian.PutUint32(mdhdPayload[8:12], timescale)
	binary.BigEndian.PutUint32(mdhdPayload[12:16], uint32(samples)*1024)
	mdhd := fullBox("mdhd", 0, 0, mdhdPayload)

	sttsPayload := make([]byte, 4+8*samples)
	binary.BigEndian.PutUint32(sttsPayload[0:4], uint32(samples))
	for i := 0; i < samples; i++ {
		binary.BigEndian.PutUint32(sttsPayload[4+8*i:8+8*i], 1)          // count
		binary.BigEndian.PutUint32(sttsPayload[8+8*i:12+8*i], 1024)      // delta
	}
	stts := fullBox("stts", 0, 0, sttsPayload)

	stszPayload := make([]byte, 8+4*samples)
	binary.BigEndian.PutUint32(stszPayload[0:4], 0)                    // uniform=0
	binary.BigEndian.PutUint32(stszPayload[4:8], uint32(samples))
	for i := 0; i < samples; i++ {
		binary.BigEndian.PutUint32(stszPayload[8+4*i:12+4*i], uint32(50+i))
	}
	stsz := fullBox("stsz", 0, 0, stszPayload)

	colrPayload := make([]byte, 11)
	copy(colrPayload[0:4], "nclx")
	binary.BigEndian.PutUint16(colrPayload[4:6], 9)
	binary.BigEndian.PutUint16(colrPayload[6:8], 16)
	binary.BigEndian.PutUint16(colrPayload[8:10], 9)
	colr := box("colr", colrPayload)

	stbl := box("stbl", append(append(stts, stsz...), colr...))
	minf := box("minf", stbl)
	mdia := box("mdia", append(mdhd, minf...))
	trak := box("trak", mdia)
	moov := box("moov", trak)
	return append(ftyp, moov...)
}
