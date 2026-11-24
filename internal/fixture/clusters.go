package fixture

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"math/rand"

	"mediacheck/ebml"
)

func writeCluster(out *bytes.Buffer, c Config, rng *rand.Rand, frame, samplesPerFrame int) {
	tc := make([]byte, 8)
	binary.BigEndian.PutUint64(tc, uint64(frame*1000/c.FPS))
	tcEl, _ := ebml.BuildElement(ebml.IDClusterTime, tc)
	video := simpleBlock(VideoTrackNumber, 0, videoFrame(c, rng, frame))
	audioStart := frame * samplesPerFrame
	audio := simpleBlock(AudioTrackNumber, 0, audioBlock(c, audioStart, samplesPerFrame))

	// CRC covers every child except the CRC element itself.
	crc := crc32.NewIEEE()
	crc.Write(tcEl)
	crc.Write(video)
	crc.Write(audio)
	crcBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(crcBuf, crc.Sum32())
	crcEl, _ := ebml.BuildElement(ebml.IDCRC32, crcBuf)

	var full bytes.Buffer
	full.Write(tcEl)
	full.Write(crcEl)
	full.Write(video)
	full.Write(audio)
	cluster, _ := ebml.BuildElement(ebml.IDCluster, full.Bytes())
	out.Write(cluster)
}

func simpleBlock(track uint32, rel int16, payload []byte) []byte {
	var b bytes.Buffer
	trackBuf, _ := ebml.EncodeSize(uint64(track), 0)
	b.Write(trackBuf)
	relBuf := []byte{byte(rel >> 8), byte(rel)}
	b.Write(relBuf)
	b.WriteByte(0x80) // keyframe flag byte
	b.Write(payload)
	el, _ := ebml.BuildElement(ebml.IDSimpleBlock, b.Bytes())
	return el
}

// videoFrame renders a deterministic RGB32 frame derived from seed and index.
func videoFrame(c Config, rng *rand.Rand, frame int) []byte {
	buf := make([]byte, c.FrameSize())
	base := byte(frame * 37)
	for i := 0; i < len(buf); i += 4 {
		pixel := i / 4
		buf[i+0] = base ^ byte(pixel*7) ^ byte(rng.Intn(256))
		buf[i+1] = byte(frame*13 + pixel)
		buf[i+2] = byte(pixel>>3) ^ byte(rng.Intn(256))
		buf[i+3] = 255
	}
	return buf
}

// audioBlock renders samplesPerFrame interleaved float32 samples.
func audioBlock(c Config, startSample, samplesPerFrame int) []byte {
	buf := make([]byte, samplesPerFrame*c.Channels*4)
	for i := 0; i < samplesPerFrame; i++ {
		for ch := 0; ch < c.Channels; ch++ {
			phase := float64(startSample+i) / float64(c.SampleRate)
			v := float32(math.Sin(2*math.Pi*440*phase) * 0.25)
			off := (i*c.Channels + ch) * 4
			binary.LittleEndian.PutUint32(buf[off:off+4], math.Float32bits(v))
		}
	}
	return buf
}
