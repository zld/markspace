package fixture

import (
	"bytes"
	"encoding/binary"
)

// boxBuffer wraps bytes.Buffer so nested box construction reads naturally
// without needing methods on the foreign buffer type.
type boxBuffer struct{ bytes.Buffer }

func (w *boxBuffer) box(typ string, payload []byte) { mp4Box(&w.Buffer, typ, payload) }

// videoStbl builds the video sample table: stsd(avc1+colr), stts, stss,
// stsc, stsz and stco. Every track is one chunk holding all samples.
func videoStbl(spec MP4Spec, samples []mp4Sample, chunkOffset uint32) []byte {
	var stbl boxBuffer
	stbl.box("stsd", videoStsd(spec))
	stbl.box("stts", sttsPayload(samples, int64(spec.VideoTimescale)/30))
	stbl.box("stss", stssPayload(samples))
	stbl.box("stsc", stscSingle(len(samples)))
	stbl.box("stsz", stszPayload(samples))
	stbl.box("stco", stcoPayload([]uint32{chunkOffset}))
	return stbl.Bytes()
}

func audioStbl(spec MP4Spec, samples []mp4Sample, chunkOffset uint32) []byte {
	var stbl boxBuffer
	stbl.box("stsd", audioStsd(spec))
	stbl.box("stts", sttsPayload(samples, int64(spec.AudioTimescale)/30/mp4AudioPerVideo))
	stbl.box("stsc", stscSingle(len(samples)))
	stbl.box("stsz", stszPayload(samples))
	stbl.box("stco", stcoPayload([]uint32{chunkOffset}))
	return stbl.Bytes()
}

func videoStsd(spec MP4Spec) []byte {
	var inner boxBuffer
	inner.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&inner.Buffer, binary.BigEndian, uint32(1))

	var avc1 boxBuffer
	avc1.Write(make([]byte, 6))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, uint16(1))
	avc1.Write(make([]byte, 16))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, mp4Fixed16(float64(spec.Width)))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, mp4Fixed16(float64(spec.Height)))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, uint32(0x00480000))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, uint32(0))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, uint16(1))
	avc1.Write(make([]byte, 32))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, uint16(0x0018))
	_ = binary.Write(&avc1.Buffer, binary.BigEndian, int16(-1))

	// Minimal self-consistent avcC: one 4-byte SPS and one 4-byte PPS.
	// Structural markers only; no decoder behavior is claimed.
	avcC := []byte{
		1,          // configurationVersion
		0x42,       // AVCProfileBaseline
		0,          // profile_compatibility
		0x1E,       // level 30
		0xFF,       // 6 reserved bits + lengthSizeMinusOne=3 (4-byte NAL len)
		0xE1,       // 3 reserved bits + numOfSequenceParameterSets=1
		0, 4,       // SPS length
		0x67, 0x42, 0x00, 0x1E, // SPS NAL (marker content)
		1,    // numOfPictureParameterSets
		0, 4, // PPS length
		0x68, 0xCE, 0x38, 0x80, // PPS NAL (marker content)
	}
	avc1.box("avcC", avcC)
	avc1.box("colr", colrPayload(spec.ColorPrim))
	inner.box("avc1", avc1.Bytes())
	return inner.Bytes()
}

func audioStsd(spec MP4Spec) []byte {
	var inner boxBuffer
	inner.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&inner.Buffer, binary.BigEndian, uint32(1))

	var mp4a boxBuffer
	mp4a.Write(make([]byte, 6))
	_ = binary.Write(&mp4a.Buffer, binary.BigEndian, uint16(1))
	mp4a.Write(make([]byte, 8))
	_ = binary.Write(&mp4a.Buffer, binary.BigEndian, uint16(2))
	_ = binary.Write(&mp4a.Buffer, binary.BigEndian, uint16(16))
	_ = binary.Write(&mp4a.Buffer, binary.BigEndian, uint16(0))
	_ = binary.Write(&mp4a.Buffer, binary.BigEndian, uint16(0))
	_ = binary.Write(&mp4a.Buffer, binary.BigEndian, uint32(spec.AudioTimescale)<<16)
	inner.box("mp4a", mp4a.Bytes())
	return inner.Bytes()
}

func colrPayload(primaries uint16) []byte {
	var b bytes.Buffer
	b.WriteString("nclx")
	_ = binary.Write(&b, binary.BigEndian, primaries)
	_ = binary.Write(&b, binary.BigEndian, uint16(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(1))
	// full_range_flag (1 bit) plus 7 reserved bits = one whole byte.
	b.WriteByte(0)
	return b.Bytes()
}

func sttsPayload(samples []mp4Sample, delta int64) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(samples)))
	_ = binary.Write(&b, binary.BigEndian, uint32(delta))
	return b.Bytes()
}

func stssPayload(samples []mp4Sample) []byte {
	var keys []uint32
	for i, s := range samples {
		if s.key {
			keys = append(keys, uint32(i+1))
		}
	}
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(keys)))
	for _, k := range keys {
		_ = binary.Write(&b, binary.BigEndian, k)
	}
	return b.Bytes()
}

func stscSingle(samplesPerChunk int) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	_ = binary.Write(&b, binary.BigEndian, uint32(samplesPerChunk))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	return b.Bytes()
}

func stszPayload(samples []mp4Sample) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(samples)))
	for _, s := range samples {
		_ = binary.Write(&b, binary.BigEndian, s.size)
	}
	return b.Bytes()
}

func stcoPayload(offsets []uint32) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(offsets)))
	for _, o := range offsets {
		_ = binary.Write(&b, binary.BigEndian, o)
	}
	return b.Bytes()
}
