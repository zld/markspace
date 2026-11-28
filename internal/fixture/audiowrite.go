package fixture

import (
	"encoding/binary"

	"github.com/pion/rtp"
)

// audioSpec describes a single-track, audio-only fixture whose samples are
// complete RTP packets.
type audioSpec struct {
	packets   int
	timescale uint32
	delta     uint32
	payload   int
	amp       func(i int) int16
}

func (a audioSpec) packetBytes(i int) []byte {
	amp := int16(0)
	if a.amp != nil {
		amp = a.amp(i)
	}
	payload := make([]byte, a.payload)
	for k := 0; k+1 < len(payload); k += 2 {
		v := amp
		if k%4 != 0 {
			v = -v
		}
		binary.LittleEndian.PutUint16(payload[k:], uint16(v))
	}
	p := rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 111, SequenceNumber: uint16(i),
		Timestamp: uint32(i) * a.delta, SSRC: 0x0badcafe,
	}, Payload: payload}
	b, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

// build writes ftyp, moov, mdat so the metadata stays readable after the tail
// of the media data is truncated (the fast-start layout).
func (a audioSpec) build(packets [][]byte) []byte {
	ftyp := rawBox("ftyp", []byte("isom"), u32(512), []byte("isomiso2mp41"))
	var data []byte
	for _, p := range packets {
		data = append(data, p...)
	}
	// Two-pass layout: moov width does not depend on the offset values, so
	// build once with zero offsets to learn its size, then fill them in.
	duration := uint32(len(packets)) * a.delta
	sizes := make([]uint32, len(packets))
	for i, p := range packets {
		sizes[i] = uint32(len(p))
	}
	probe := a.buildMoov(make([]uint32, len(packets)), sizes, duration)
	start := len(ftyp) + len(probe) + 8
	offs := make([]uint32, len(packets))
	pos := start
	for i, p := range packets {
		offs[i] = uint32(pos)
		pos += len(p)
	}
	moov := a.buildMoov(offs, sizes, duration)
	return cat(ftyp, moov, rawBox("mdat", data))
}

func (a audioSpec) buildMoov(offs []uint32, sizes []uint32, duration uint32) []byte {
	tkhd := fullBox("tkhd", 0, 3,
		u32(0), u32(0), u32(1), u32(0), u32(duration),
		u32(0), u32(0), u16(0), u16(0), u16(0x0100), u16(0),
		matrixBytes(matrixForRotation(0)), u32(0), u32(0))
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(a.timescale), u32(duration), u16(0x55c4), u16(0))
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte("soun"), repeat(0, 12))
	smhd := fullBox("smhd", 0, 0, u16(0), u16(0))
	mp4a := rawBox("mp4a",
		repeat(0, 6), u16(1), u16(0), u16(0), u16(0), u16(0),
		u16(2), u16(16), u16(0), u16(0), u32(a.timescale<<16))
	stsd := fullBox("stsd", 0, 0, u32(1), mp4a)
	count := len(offs)
	stts := fullBox("stts", 0, 0, u32(1), u32(uint32(count)), u32(a.delta))
	stsc := fullBox("stsc", 0, 0, u32(1), u32(1), u32(1), u32(1))
	stszPayload := cat(u32(0), u32(uint32(count)))
	for i := 0; i < count; i++ {
		stszPayload = append(stszPayload, u32(sizes[i])...)
	}
	stsz := fullBox("stsz", 0, 0, stszPayload)
	stcoPayload := u32(uint32(len(offs)))
	for _, o := range offs {
		stcoPayload = append(stcoPayload, u32(o)...)
	}
	stco := fullBox("stco", 0, 0, stcoPayload)
	stbl := rawBox("stbl", stsd, stts, stsc, stsz, stco)
	minf := rawBox("minf", smhd, rawBox("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))), stbl)
	mdia := rawBox("mdia", mdhd, hdlr, minf)
	trak := rawBox("trak", tkhd, mdia)
	mvhd := fullBox("mvhd", 0, 0, u32(0), u32(0), u32(1000), u32(duration*1000/a.timescale),
		i32(0x00010000), u16(0x0100), u16(0), u32(0), u32(0),
		matrixBytes(matrixForRotation(0)), repeat(0, 24), u32(2))
	return rawBox("moov", mvhd, trak)
}

// AudioSpecForTest builds a single-track audio fixture spec with one 100 ms
// packet per timescale step, so silence transitions land exactly on window
// boundaries in tests.
func AudioSpecForTest(packets int, amp func(i int) int16) audioSpec {
	return audioSpec{packets: packets, timescale: 1000, delta: 100, payload: 160, amp: amp}
}

// WriteAudioMP4 writes an audio-only fixture.
func WriteAudioMP4(path string, spec audioSpec) error {
	pkts := make([][]byte, spec.packets)
	for i := range pkts {
		pkts[i] = spec.packetBytes(i)
	}
	return WriteFile(path, spec.build(pkts))
}

// WriteRawAudioPackets writes an audio-only fixture whose sample bytes carry no
// RTP header. Repeating an identical payload lets fingerprint tests exercise
// genuine content duplication.
func WriteRawAudioPackets(path string, timescale, delta uint32, payload []byte, n int) error {
	packets := make([][]byte, n)
	for i := range packets {
		packets[i] = append([]byte(nil), payload...)
	}
	return WriteFile(path, audioSpec{packets: n, timescale: timescale, delta: delta}.build(packets))
}

// WriteAudioPackets writes an audio-only fixture from prebuilt RTP packets at a
// fixed timescale and sample delta.
func WriteAudioPackets(path string, timescale, delta uint32, packets [][]byte) error {
	return WriteFile(path, audioSpec{packets: len(packets), timescale: timescale, delta: delta}.build(packets))
}
