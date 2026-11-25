// Package fixture generates small, deterministic local MP4/MOV and RTP
// fixtures. No sample media is read from outside the repository.
package fixture

import (
	"encoding/binary"
)

// TrackSpec describes one generated track. One stts entry per delta keeps
// variable frame durations exact.
type TrackSpec struct {
	TrackID   uint32
	Timescale uint32
	Duration  uint32
	Deltas    []uint32
}

// MP4Spec is the minimal MP4 generation request.
type MP4Spec struct {
	MajorBrand     string
	MovieTimescale uint32
	MovieDuration  uint32
	Tracks         []TrackSpec
	MediaPayload   []byte
}

// BuildMP4 builds a small but go-mp4-probe-compatible MP4 file.
func BuildMP4(spec MP4Spec) []byte {
	ftyp := buildFtyp(spec.MajorBrand)
	moovZero := buildMoov(spec, 0)
	mdatPayload := spec.MediaPayload
	if mdatPayload == nil {
		mdatPayload = []byte("replaycheck-fixture")
	}
	stcoOffset := uint32(len(ftyp) + len(moovZero) + 8)
	moov := buildMoov(spec, stcoOffset)
	mdat := box("mdat", mdatPayload)

	out := make([]byte, 0, len(ftyp)+len(moov)+len(mdat))
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = append(out, mdat...)
	return out
}

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return append(head, payload...)
}

func buildFtyp(major string) []byte {
	if len(major) != 4 {
		major = "isom"
	}
	p := make([]byte, 0, 16)
	p = append(p, major...)
	p = put32(p, 0x00000200)
	p = append(p, "isom"...)
	p = append(p, "mp42"...)
	return box("ftyp", p)
}

func put32(dst []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return append(dst, b...)
}

func put16(dst []byte, v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return append(dst, b...)
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	binary.BigEndian.PutUint32(m[0:4], 0x00010000)
	binary.BigEndian.PutUint32(m[16:20], 0x00010000)
	binary.BigEndian.PutUint32(m[32:36], 0x40000000)
	return m
}

func buildMvhd(timescale, duration uint32) []byte {
	p := []byte{}
	p = put32(p, 0) // creation
	p = put32(p, 0) // modification
	p = put32(p, timescale)
	p = put32(p, duration)
	p = put32(p, 0x00010000) // rate 1.0
	p = put16(p, 0x0100)     // volume 1.0
	p = append(p, make([]byte, 10)...)
	p = append(p, identityMatrix()...)
	p = append(p, make([]byte, 24)...) // predefined
	p = put32(p, 2)                    // next track ID
	return box("mvhd", fullBox(0, 0, p))
}

func buildTkhd(id, duration uint32) []byte {
	p := []byte{}
	p = put32(p, 0)
	p = put32(p, 0)
	p = put32(p, id)
	p = put32(p, 0)
	p = put32(p, duration)
	p = append(p, make([]byte, 8)...)
	p = put16(p, 0) // layer
	p = put16(p, 0) // alternate group
	p = put16(p, 0) // volume
	p = put16(p, 0) // reserved
	p = append(p, identityMatrix()...)
	p = put32(p, 320<<16) // width
	p = put32(p, 180<<16) // height
	return box("tkhd", fullBox(0, 3, p))
}

func buildMdhd(timescale, duration uint32) []byte {
	p := []byte{}
	p = put32(p, 0)
	p = put32(p, 0)
	p = put32(p, timescale)
	p = put32(p, duration)
	p = put16(p, 0x55c4) // language "und"
	p = put16(p, 0)
	return box("mdhd", fullBox(0, 0, p))
}

func buildStts(deltas []uint32) []byte {
	p := []byte{}
	p = put32(p, uint32(len(deltas)))
	for _, d := range deltas {
		p = put32(p, 1)
		p = put32(p, d)
	}
	return box("stts", fullBox(0, 0, p))
}

func buildStsc(sampleCount uint32) []byte {
	p := []byte{}
	p = put32(p, 1)
	p = put32(p, 1)
	p = put32(p, sampleCount)
	p = put32(p, 1)
	return box("stsc", fullBox(0, 0, p))
}

func buildStco(offset uint32) []byte {
	p := []byte{}
	p = put32(p, 1)
	p = put32(p, offset)
	return box("stco", fullBox(0, 0, p))
}

func buildStsd() []byte {
	p := put32([]byte{}, 0) // no sample descriptions; timing boxes remain valid
	return box("stsd", fullBox(0, 0, p))
}

func buildTrak(track TrackSpec, chunkOffset uint32) []byte {
	n := uint32(len(track.Deltas))
	stbl := []byte{}
	stbl = append(stbl, buildStsd()...)
	stbl = append(stbl, buildStts(track.Deltas)...)
	stbl = append(stbl, buildStsc(n)...)
	stbl = append(stbl, buildStco(chunkOffset)...)

	minf := box("minf", stbl)
	mdia := box("mdia", append(buildMdhd(track.Timescale, track.Duration), minf...))
	trakPayload := append(buildTkhd(track.TrackID, track.Duration), mdia...)
	return box("trak", trakPayload)
}

func buildMoov(spec MP4Spec, chunkOffset uint32) []byte {
	payload := buildMvhd(spec.MovieTimescale, spec.MovieDuration)
	for _, track := range spec.Tracks {
		payload = append(payload, buildTrak(track, chunkOffset)...)
	}
	return box("moov", payload)
}
