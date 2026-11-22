// Package fixtures deterministically generates the local sample media
// set (MP4 and MXF subset files) plus the fault-injection samples.
// All bytes derive only from the seed, so regeneration is reproducible
// and no external media is ever referenced.
package fixtures

import (
	"crypto/sha256"
	"fmt"

	"archive/internal/parse"

	"github.com/pion/rtp"
)

// Layout inside the output directory.
const (
	TracksDir  = "in"
	CorruptDir = "corrupt"
)

type trackSpec struct {
	name          string
	format        string // "mp4" or "mxf"
	trackID       int
	rateNum       uint32 // mp4 timescale or mxf edit-rate numerator
	rateDen       uint32 // mxf edit-rate denominator (mp4: 1)
	delta         uint32 // sample duration in track ticks
	mediaOffset   int64  // non-zero start, track ticks
	rotation      uint32 // degrees
	width, height uint32
	crop          *[4]uint32 // x, y, w, h
	colors        [][3]uint16
	rtpBase       uint32
	samples       int
}

// trackSpecs describes the 8 generated tracks: mixed containers,
// rotations, timescales, non-zero starts, crops and colour conflicts.
func trackSpecs() []trackSpec {
	bt709 := [3]uint16{1, 1, 1}
	bt601 := [3]uint16{5, 6, 6}
	return []trackSpec{
		{name: "track0.mp4", format: "mp4", trackID: 1, rateNum: 90000, rateDen: 1, delta: 3000, rotation: 0, width: 320, height: 240, colors: [][3]uint16{bt709}, rtpBase: 0, samples: 3},
		{name: "track1.mp4", format: "mp4", trackID: 2, rateNum: 25, rateDen: 1, delta: 1, rotation: 90, width: 320, height: 240, crop: &[4]uint32{8, 8, 300, 220}, colors: [][3]uint16{bt709}, rtpBase: 5400000, samples: 3},
		{name: "track2.mp4", format: "mp4", trackID: 3, rateNum: 30000, rateDen: 1, delta: 1001, mediaOffset: 7, rotation: 180, width: 320, height: 240, colors: [][3]uint16{bt709}, rtpBase: 10800000, samples: 3},
		{name: "track3.mp4", format: "mp4", trackID: 4, rateNum: 24, rateDen: 1, delta: 1, rotation: 270, width: 320, height: 240, colors: [][3]uint16{bt601, bt709}, rtpBase: 16200000, samples: 3},
		{name: "track4.mxf", format: "mxf", trackID: 5, rateNum: 25, rateDen: 1, delta: 1, mediaOffset: 50, rotation: 0, width: 320, height: 240, crop: &[4]uint32{0, 0, 320, 200}, colors: [][3]uint16{bt709}, rtpBase: 21600000, samples: 3},
		{name: "track5.mxf", format: "mxf", trackID: 6, rateNum: 30, rateDen: 1, delta: 1, rotation: 90, width: 320, height: 240, colors: [][3]uint16{bt709}, rtpBase: 27000000, samples: 3},
		{name: "track6.mxf", format: "mxf", trackID: 7, rateNum: 90000, rateDen: 1, delta: 3000, rotation: 180, width: 320, height: 240, colors: [][3]uint16{bt601, bt709}, rtpBase: 32400000, samples: 3},
		{name: "track7.mxf", format: "mxf", trackID: 8, rateNum: 25, rateDen: 1, delta: 1, rotation: 270, width: 320, height: 240, crop: &[4]uint32{4, 4, 312, 232}, colors: [][3]uint16{bt709}, rtpBase: 37800000, samples: 3},
	}
}

// payload derives deterministic sample bytes from the seed.
func payload(seed int64, track, sample, size int) []byte {
	b := make([]byte, size)
	for i := 0; i < size; i += 32 {
		h := sha256.Sum256([]byte(fmt.Sprintf("payload/%d/%d/%d/%d", seed, track, sample, i)))
		copy(b[i:], h[:])
	}
	return b
}

func sampleSize(track, sample int) uint32 { return uint32(64 + ((track+sample)%3)*16) }

func rtpPacket(ts uint32) []byte {
	pkt := rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 1, Timestamp: ts, SSRC: 0x11447},
		Payload: []byte("sync0000"),
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// --- minimal MP4 box writer (fixture generation only) ---

func u16(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }

func u32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

func box(typ string, payload []byte) []byte {
	b := append(u32(uint32(8+len(payload))), []byte(typ)...)
	return append(b, payload...)
}

func fullBox(typ string, version byte, payload []byte) []byte {
	return box(typ, append([]byte{version, 0, 0, 0}, payload...))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func buildMP4(seed int64, tIdx int, s trackSpec) []byte {
	n := s.samples
	sizes := make([]uint32, n)
	var media []byte
	for i := 0; i < n; i++ {
		sizes[i] = sampleSize(tIdx, i)
		media = append(media, payload(seed, tIdx, i, int(sizes[i]))...)
	}
	matrix, err := parse.MatrixForRotation(s.rotation)
	if err != nil {
		panic(err)
	}
	tkhd := concat(
		u32(0), u32(0), u32(uint32(s.trackID)), u32(0), u32(s.delta*uint32(n)),
		u32(0), u32(0),
		u16(0), u16(0), u16(0), u16(0),
	)
	for _, m := range matrix {
		tkhd = append(tkhd, u32(uint32(m))...)
	}
	tkhd = append(tkhd, concat(u32(s.width<<16), u32(s.height<<16))...)
	mdhd := fullBox("mdhd", 0, concat(u32(0), u32(0), u32(s.rateNum), u32(s.delta*uint32(n)), u16(0), u16(0)))
	stts := fullBox("stts", 0, concat(u32(1), u32(uint32(n)), u32(s.delta)))
	sz := concat(u32(0), u32(uint32(n)))
	for _, v := range sizes {
		sz = append(sz, u32(v)...)
	}
	stsz := fullBox("stsz", 0, sz)
	stsc := fullBox("stsc", 0, concat(u32(1), u32(1), u32(uint32(n)), u32(1)))
	stssBody := u32(uint32(n))
	for i := 0; i < n; i++ {
		stssBody = append(stssBody, u32(uint32(i+1))...)
	}
	stss := fullBox("stss", 0, stssBody)
	var extra []byte
	for _, c := range s.colors {
		extra = append(extra, box("colr", concat([]byte("nclx"), u16(c[0]), u16(c[1]), u16(c[2]), []byte{0x80}))...)
	}
	if s.crop != nil {
		extra = append(extra, box("crop", concat(u32(s.crop[0]), u32(s.crop[1]), u32(s.crop[2]), u32(s.crop[3])))...)
	}
	extra = append(extra, box("rtps", rtpPacket(s.rtpBase))...)
	var edts []byte
	if s.mediaOffset != 0 {
		elst := fullBox("elst", 0, concat(u32(1), u32(s.delta*uint32(n)), u32(uint32(s.mediaOffset)), u16(1), u16(0)))
		edts = box("edts", elst)
	}
	ftyp := box("ftyp", concat([]byte("isom"), u32(0), []byte("isom")))
	makeMoov := func(chunkOff uint32) []byte {
		stco := fullBox("stco", 0, concat(u32(1), u32(chunkOff)))
		stbl := box("stbl", concat(stts, stsz, stsc, stco, stss, extra))
		minf := box("minf", stbl)
		mdia := box("mdia", concat(mdhd, minf))
		trak := box("trak", concat(fullBox("tkhd", 0, tkhd), edts, mdia))
		return box("moov", trak)
	}
	moov := makeMoov(0)
	chunkOff := uint32(len(ftyp) + len(moov) + 8)
	moov = makeMoov(chunkOff)
	return concat(ftyp, moov, box("mdat", media))
}
