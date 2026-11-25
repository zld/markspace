package media

import (
	"bytes"
	"encoding/binary"
)

// MP4Spec describes the tiny code-generated audio MP4 used by tests/demo.
type MP4Spec struct {
	TrackID       uint32
	Timescale     uint32
	DurationTicks uint32
	Channels      uint16
	SampleRate    uint32
}

// GenerateMinimalMP4 builds a small but probe-valid MP4 with one audio track.
// It exists so acceptance never depends on an external media fixture.
func GenerateMinimalMP4(spec MP4Spec) []byte {
	if spec.TrackID == 0 {
		spec.TrackID = 1
	}
	if spec.Timescale == 0 {
		spec.Timescale = 48000
	}
	if spec.DurationTicks == 0 {
		spec.DurationTicks = spec.Timescale
	}
	if spec.Channels == 0 {
		spec.Channels = 2
	}
	if spec.SampleRate == 0 {
		spec.SampleRate = 48000
	}

	ftyp := boxBytes("ftyp", ftypPayload())
	moovPlaceholder := boxBytes("moov", buildMoov(spec, 0))
	chunkOffset := uint32(len(ftyp) + len(moovPlaceholder) + 8) // mdat header is 8 bytes
	moov := boxBytes("moov", buildMoov(spec, chunkOffset))
	mdat := boxBytes("mdat", []byte("ARCHIVE-SAMPLE-AUDIO"))

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	out.Write(mdat)
	return out.Bytes()
}

func ftypPayload() []byte {
	var p []byte
	p = appendFourCC(p, "isom")
	p = appendU32(p, 512)
	p = appendFourCC(p, "isom")
	return p
}

func buildMoov(spec MP4Spec, chunkOffset uint32) []byte {
	mvhd := boxBytes("mvhd", mvhdPayload(spec))
	trak := boxBytes("trak", buildTrak(spec, chunkOffset))
	return append(mvhd, trak...)
}

func mvhdPayload(spec MP4Spec) []byte {
	p := fullBoxPayload(0, 0)
	p = appendU32(p, 0) // creation
	p = appendU32(p, 0) // modification
	p = appendU32(p, 1000)
	p = appendU32(p, uint32(uint64(spec.DurationTicks)*1000/uint64(spec.Timescale)))
	p = appendU32(p, 0x00010000) // rate 1.0
	p = appendU16(p, 0x0100)     // volume 1.0
	p = appendZeros(p, 10)
	p = appendIdentityMatrix(p)
	p = appendZeros(p, 24) // pre-defined
	p = appendU32(p, spec.TrackID+1)
	return p
}

func buildTrak(spec MP4Spec, chunkOffset uint32) []byte {
	tkhd := boxBytes("tkhd", tkhdPayload(spec))
	mdhd := boxBytes("mdhd", mdhdPayload(spec))
	stbl := boxBytes("stbl", buildStbl(spec, chunkOffset))
	minf := boxBytes("minf", stbl)
	mdia := boxBytes("mdia", append(mdhd, minf...))
	return append(tkhd, mdia...)
}

func tkhdPayload(spec MP4Spec) []byte {
	p := fullBoxPayload(0, 7) // enabled/in-use flags
	p = appendU32(p, 0)
	p = appendU32(p, 0)
	p = appendU32(p, spec.TrackID)
	p = appendU32(p, 0) // reserved
	p = appendU32(p, uint32(uint64(spec.DurationTicks)*1000/uint64(spec.Timescale)))
	p = appendZeros(p, 8)
	p = appendU16(p, 0) // layer
	p = appendU16(p, 0) // alternate group
	p = appendU16(p, 0x0100)
	p = appendU16(p, 0)
	p = appendIdentityMatrix(p)
	p = appendU32(p, 0) // width
	p = appendU32(p, 0) // height
	return p
}

func mdhdPayload(spec MP4Spec) []byte {
	p := fullBoxPayload(0, 0)
	p = appendU32(p, 0)
	p = appendU32(p, 0)
	p = appendU32(p, spec.Timescale)
	p = appendU32(p, spec.DurationTicks)
	p = appendU16(p, 0) // language
	p = appendU16(p, 0)
	return p
}

func buildStbl(spec MP4Spec, chunkOffset uint32) []byte {
	mp4a := boxBytes("mp4a", mp4aPayload(spec))
	stsdPayload := fullBoxPayload(0, 0)
	stsdPayload = appendU32(stsdPayload, 1)
	stsdPayload = append(stsdPayload, mp4a...)
	stsd := boxBytes("stsd", stsdPayload)

	sttsPayload := fullBoxPayload(0, 0)
	sttsPayload = appendU32(sttsPayload, 1)
	sttsPayload = appendU32(sttsPayload, 1)
	sttsPayload = appendU32(sttsPayload, spec.DurationTicks)
	stts := boxBytes("stts", sttsPayload)

	stscPayload := fullBoxPayload(0, 0)
	stscPayload = appendU32(stscPayload, 1)
	stscPayload = appendU32(stscPayload, 1)
	stscPayload = appendU32(stscPayload, 1)
	stscPayload = appendU32(stscPayload, 1)
	stsc := boxBytes("stsc", stscPayload)

	stszPayload := fullBoxPayload(0, 0)
	stszPayload = appendU32(stszPayload, 0)
	stszPayload = appendU32(stszPayload, 1)
	stszPayload = appendU32(stszPayload, 21)
	stsz := boxBytes("stsz", stszPayload)

	stcoPayload := fullBoxPayload(0, 0)
	stcoPayload = appendU32(stcoPayload, 1)
	stcoPayload = appendU32(stcoPayload, chunkOffset)
	stco := boxBytes("stco", stcoPayload)

	var out []byte
	for _, b := range [][]byte{stsd, stts, stsc, stsz, stco} {
		out = append(out, b...)
	}
	return out
}

func mp4aPayload(spec MP4Spec) []byte {
	p := appendZeros(nil, 6) // reserved SampleEntry
	p = appendU16(p, 1)      // data reference index
	p = appendU16(p, 0)      // entry version
	p = appendZeros(p, 6)    // reserved
	p = appendU16(p, spec.Channels)
	p = appendU16(p, 16) // sample size
	p = appendU16(p, 0)  // predefined
	p = appendU16(p, 0)  // reserved
	p = appendU32(p, spec.SampleRate<<16)
	return p
}

func boxBytes(typ string, payload []byte) []byte {
	out := make([]byte, 0, 8+len(payload))
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(8+len(payload)))
	out = append(out, size[:]...)
	out = appendFourCC(out, typ)
	out = append(out, payload...)
	return out
}

func fullBoxPayload(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func appendU16(dst []byte, v uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return append(dst, b[:]...)
}

func appendU32(dst []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(dst, b[:]...)
}

func appendFourCC(dst []byte, s string) []byte {
	var b [4]byte
	for i := 0; i < 4; i++ {
		if i < len(s) {
			b[i] = s[i]
		}
	}
	return append(dst, b[:]...)
}

func appendZeros(dst []byte, n int) []byte {
	return append(dst, make([]byte, n)...)
}

func appendIdentityMatrix(dst []byte) []byte {
	values := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for _, v := range values {
		dst = appendU32(dst, v)
	}
	return dst
}
