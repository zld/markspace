package testfixture

import (
	"encoding/binary"
)

func mp4Type(s string) [4]byte {
	var t [4]byte
	copy(t[:], s)
	return t
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(typ string, fields []byte) []byte {
	payload := make([]byte, 4+len(fields))
	// version=0, flags=0
	copy(payload[4:], fields)
	return box(typ, payload)
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func appendBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// BuildMinimalMP4 builds a small, non-playback MP4 with a valid sample table
// pointing at concatenated sample payloads. It is sufficient for go-mp4's
// Probe and offset validation used by this project.
func BuildMinimalMP4(samples [][]byte, timescale uint32, delta uint32) ([]byte, error) {
	sizes := make([]uint32, len(samples))
	var payload []byte
	for i, s := range samples {
		sizes[i] = uint32(len(s))
		payload = append(payload, s...)
	}

	ftypPayload := appendBytes(
		[]byte("isom"),
		u32(0x200),
		[]byte("isom"),
	)
	ftyp := box("ftyp", ftypPayload)

	tkhdFields := make([]byte, 80)                   // after version/flags: total box payload 84
	binary.BigEndian.PutUint32(tkhdFields[8:12], 1)  // track ID
	binary.BigEndian.PutUint32(tkhdFields[12:16], 0) // reserved
	binary.BigEndian.PutUint32(tkhdFields[16:20], uint32(len(samples))*delta)
	// layer, alternate group, volume, reserved remain zero
	// identity matrix at field offset 36.
	matrixOff := 36
	binary.BigEndian.PutUint32(tkhdFields[matrixOff+0:], 0x00010000)
	binary.BigEndian.PutUint32(tkhdFields[matrixOff+16:], 0x00010000)
	binary.BigEndian.PutUint32(tkhdFields[matrixOff+32:], 0x40000000)
	binary.BigEndian.PutUint32(tkhdFields[72:76], uint32(16<<16)) // width
	binary.BigEndian.PutUint32(tkhdFields[76:80], uint32(16<<16)) // height
	tkhd := fullBox("tkhd", tkhdFields)

	mdhdFields := appendBytes(
		u32(0), u32(0),
		u32(timescale),
		u32(uint32(len(samples))*delta),
		u16(0x55c4), // language "und"
		u16(0),
	)
	mdhd := fullBox("mdhd", mdhdFields)

	sttsFields := appendBytes(
		u32(1),
		u32(uint32(len(samples))),
		u32(delta),
	)
	stts := fullBox("stts", sttsFields)

	stscFields := appendBytes(
		u32(1),
		u32(1), // first chunk
		u32(uint32(len(samples))),
		u32(1), // sample description index
	)
	stsc := fullBox("stsc", stscFields)

	stszFields := appendBytes(u32(0), u32(uint32(len(samples))))
	for _, size := range sizes {
		stszFields = append(stszFields, u32(size)...)
	}
	stsz := fullBox("stsz", stszFields)

	buildMoov := func(chunkOffset uint32) []byte {
		stco := fullBox("stco", appendBytes(u32(1), u32(chunkOffset)))
		stbl := box("stbl", appendBytes(stts, stsc, stsz, stco))
		minf := box("minf", stbl)
		mdia := box("mdia", appendBytes(mdhd, minf))
		trak := box("trak", appendBytes(tkhd, mdia))
		return box("moov", trak)
	}

	moovSizing := buildMoov(0)
	chunkOffset := uint32(8 + len(ftyp) + len(moovSizing))
	moov := buildMoov(chunkOffset)
	mdat := box("mdat", payload)
	return appendBytes(ftyp, moov, mdat), nil
}
