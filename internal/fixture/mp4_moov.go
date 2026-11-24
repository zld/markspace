package fixture

import (
	"bytes"
	"encoding/binary"
)

type mp4Sample struct {
	trackID   uint32
	size      uint32
	key       bool
	scaleTick int64
}

// buildMoov assembles the moov box. Each track is a single chunk whose
// mdat offset is passed in once the ftyp|moov|mdat layout is known.
func buildMoov(spec MP4Spec, vid, aud []mp4Sample, videoChunk, audioChunk uint32) []byte {
	var moov boxBuffer
	moov.box("mvhd", mvhdPayload(spec))
	moov.box("trak", videoTrak(spec, vid, videoChunk))
	moov.box("trak", audioTrak(spec, aud, audioChunk))
	var out bytes.Buffer
	mp4Box(&out, "moov", moov.Bytes())
	return out.Bytes()
}

func mvhdPayload(spec MP4Spec) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, uint32(1000))
	_ = binary.Write(&b, binary.BigEndian, uint32(spec.VideoFrames*1000/30))
	_ = binary.Write(&b, binary.BigEndian, uint32(0x00010000))
	_ = binary.Write(&b, binary.BigEndian, uint16(0x0100))
	b.Write([]byte{0, 0})
	b.Write(make([]byte, 8))
	for _, v := range identityMatrix {
		_ = binary.Write(&b, binary.BigEndian, v)
	}
	b.Write(make([]byte, 24))
	_ = binary.Write(&b, binary.BigEndian, uint32(3))
	return b.Bytes()
}

func videoTrak(spec MP4Spec, samples []mp4Sample, chunkOffset uint32) []byte {
	var trak boxBuffer
	trak.box("tkhd", tkhdPayload(spec, 1, uint32(spec.VideoFrames*1000/30), true))
	var mdia boxBuffer
	mdia.box("mdhd", mdhdPayload(spec.VideoTimescale,
		uint32(int64(spec.VideoFrames)*int64(spec.VideoTimescale)/30)))
	mdia.box("hdlr", hdlrPayload(mp4VideoHandler, "VideoHandler"))
	var minf boxBuffer
	minf.box("vmhd", mp4FullBox(0, 1, make([]byte, 8)))
	minf.box("dinf", dinfPayload())
	minf.box("stbl", videoStbl(spec, samples, chunkOffset))
	mdia.box("minf", minf.Bytes())
	trak.box("mdia", mdia.Bytes())
	return trak.Bytes()
}

func audioTrak(spec MP4Spec, samples []mp4Sample, chunkOffset uint32) []byte {
	var trak boxBuffer
	trak.box("tkhd", tkhdPayload(spec, 2, uint32(spec.VideoFrames*1000/30), false))
	var mdia boxBuffer
	audioDuration := uint32(int64(len(samples)) * int64(spec.AudioTimescale) / 30 / mp4AudioPerVideo)
	mdia.box("mdhd", mdhdPayload(spec.AudioTimescale, audioDuration))
	mdia.box("hdlr", hdlrPayload(mp4AudioHandler, "SoundHandler"))
	var minf boxBuffer
	minf.box("smhd", mp4FullBox(0, 0, make([]byte, 4)))
	minf.box("dinf", dinfPayload())
	minf.box("stbl", audioStbl(spec, samples, chunkOffset))
	mdia.box("minf", minf.Bytes())
	trak.box("mdia", mdia.Bytes())
	return trak.Bytes()
}

func tkhdPayload(spec MP4Spec, trackID, durationMS uint32, video bool) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 3, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, trackID)
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, durationMS)
	b.Write(make([]byte, 8))
	_ = binary.Write(&b, binary.BigEndian, uint16(0))
	_ = binary.Write(&b, binary.BigEndian, uint16(0))
	_ = binary.Write(&b, binary.BigEndian, uint16(0))
	b.Write([]byte{0, 0})
	matrix := identityMatrix
	if video {
		matrix = rotationMatrix(spec.Rotation)
	}
	for _, v := range matrix {
		_ = binary.Write(&b, binary.BigEndian, v)
	}
	if video {
		_ = binary.Write(&b, binary.BigEndian, mp4Fixed16(float64(spec.Width)))
		_ = binary.Write(&b, binary.BigEndian, mp4Fixed16(float64(spec.Height)))
	} else {
		b.Write(make([]byte, 8))
	}
	return b.Bytes()
}

func mdhdPayload(timescale, duration uint32) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_ = binary.Write(&b, binary.BigEndian, timescale)
	_ = binary.Write(&b, binary.BigEndian, duration)
	b.Write([]byte{0x55, 0xC4})
	b.Write([]byte{0, 0})
	return b.Bytes()
}

func hdlrPayload(handler, name string) []byte {
	var b bytes.Buffer
	b.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	b.WriteString(handler)
	b.Write(make([]byte, 12))
	b.WriteString(name)
	b.WriteByte(0)
	return b.Bytes()
}

func dinfPayload() []byte {
	var dref boxBuffer
	dref.Write(mp4FullBox(0, 0, nil))
	_ = binary.Write(&dref.Buffer, binary.BigEndian, uint32(1))
	dref.box("url ", mp4FullBox(0, 1, nil))
	var b boxBuffer
	b.box("dref", dref.Bytes())
	return b.Bytes()
}
