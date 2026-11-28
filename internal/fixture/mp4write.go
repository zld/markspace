package fixture

import (
	"encoding/binary"
	"os"
)

// BuildMP4 renders a complete two-track (video + audio) MP4 from the spec and
// returns the bytes. Layout regions are computed first so chunk offsets written
// into stco match the final file exactly.
type mp4Layout struct {
	videoData [][]byte
	audioData [][]byte
	moov      []byte
	mdatStart int
}

func (s mp4Spec) buildMP4() ([]byte, *mp4Layout) {
	vd := make([][]byte, s.videoSamples)
	for i := range vd {
		vd[i] = s.videoBytes(i)
	}
	ad := s.audioPacketsBytes()

	// Order inside mdat: all video samples, then all audio packets.
	var data []byte
	for _, b := range vd {
		data = append(data, b...)
	}
	audioStart := len(data)
	for _, b := range ad {
		data = append(data, b...)
	}
	_ = audioStart

	// ftyp + mdat header precedes mdat payload.
	ftyp := rawBox("ftyp", []byte("isom"), u32(512), []byte("isomiso2mp41"))
	mdatPayload := cat(data)
	mdatHeader := 8
	mdatStart := len(ftyp) + mdatHeader

	videoChunkOff := make([]uint32, len(vd))
	off := mdatStart
	for i, b := range vd {
		videoChunkOff[i] = uint32(off)
		off += len(b)
	}
	audioChunkOff := make([]uint32, len(ad))
	for i, b := range ad {
		audioChunkOff[i] = uint32(off)
		off += len(b)
	}

	moov := s.buildMoov(videoChunkOff, audioChunkOff)
	out := cat(ftyp, rawBox("mdat", mdatPayload), moov)
	return out, &mp4Layout{videoData: vd, audioData: ad, moov: moov, mdatStart: mdatStart}
}

func (s mp4Spec) durationTicks(samples, delta uint32) uint32 { return samples * delta }

// buildMP4VideoOnly renders a fixture with just the video track, which keeps a
// video-only sample set from carrying an empty audio lane.
func (s mp4Spec) buildMP4VideoOnly() ([]byte, *mp4Layout) {
	vd := make([][]byte, s.videoSamples)
	for i := range vd {
		vd[i] = s.videoBytes(i)
	}
	ftyp := rawBox("ftyp", []byte("isom"), u32(512), []byte("isomiso2mp41"))
	var data []byte
	for _, b := range vd {
		data = append(data, b...)
	}
	mdatStart := len(ftyp) + 8
	offs := make([]uint32, len(vd))
	pos := mdatStart
	for i, b := range vd {
		offs[i] = uint32(pos)
		pos += len(b)
	}
	duration := s.durationTicks(uint32(s.videoSamples), s.videoDelta)
	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), u32(1000), u32(duration*1000/s.videoTimescale),
		i32(0x00010000), u16(0x0100), u16(0), u32(0), u32(0),
		matrixBytes(matrixForRotation(0)), repeat(0, 24), u32(2))
	moov := rawBox("moov", mvhd, s.buildVideoTrak(offs, duration))
	out := cat(ftyp, rawBox("mdat", data), moov)
	return out, &mp4Layout{videoData: vd, moov: moov, mdatStart: mdatStart}
}

func (s mp4Spec) buildMoov(videoChunkOff, audioChunkOff []uint32) []byte {
	videoDuration := s.durationTicks(uint32(s.videoSamples), s.videoDelta)
	audioDuration := s.durationTicks(uint32(s.audioPackets), s.audioDelta)

	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), u32(1000), u32(videoDuration*1000/s.videoTimescale),
		i32(0x00010000), u16(0x0100), u16(0), u32(0), u32(0),
		matrixBytes(matrixForRotation(0)), repeat(0, 24), u32(3))

	trakV := s.buildVideoTrak(videoChunkOff, videoDuration)
	trakA := s.buildAudioTrak(audioChunkOff, audioDuration)
	return rawBox("moov", mvhd, trakV, trakA)
}

func (s mp4Spec) buildVideoTrak(chunkOffs []uint32, duration uint32) []byte {
	w, h := uint32(1920), uint32(1080)
	tkhd := fullBox("tkhd", 0, 3,
		u32(0), u32(0), u32(1), u32(0), u32(duration),
		u32(0), u32(0), u16(0), u16(0), u16(0), u16(0),
		matrixBytes(matrixForRotation(s.rotation)),
		u32(w<<16), u32(h<<16))
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(s.videoTimescale), u32(duration), u16(0x55c4), u16(0))
	// Handler name is optional in ISO-BMFF; omitting it avoids a trailing
	// string field that some readers treat as truncated.
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte("vide"), repeat(0, 12))
	vmhd := fullBox("vmhd", 0, 1, u16(0), u16(0), u16(0), u16(0))

	// Visual sample entry with crop and color children.
	var entryChildren [][]byte
	if s.crop != nil {
		entryChildren = append(entryChildren, s.crop.bytes())
	}
	for _, c := range s.colors {
		entryChildren = append(entryChildren, c.bytes())
	}
	entryPrelude := cat(
		repeat(0, 6), u16(1), // SampleEntry
		u16(0), u16(0), repeat(0, 12), // PreDefined, Reserved, PreDefined2
		u16(uint16(w)), u16(uint16(h)),
		u32(72<<16), u32(72<<16), u32(0),
		u16(1), repeat(0, 32), u16(0x0018), u16(0xffff))
	avc1 := rawBox("avc1", entryPrelude, cat(entryChildren...))

	stsd := fullBox("stsd", 0, 0, u32(1), avc1)
	stts := fullBox("stts", 0, 0, u32(1), u32(uint32(s.videoSamples)), u32(s.videoDelta))
	stsc := fullBox("stsc", 0, 0, u32(1), u32(1), u32(1), u32(1))
	// One sample per chunk: SampleSize constant.
	stsz := fullBox("stsz", 0, 0, u32(uint32(s.videoSampleLen)), u32(uint32(s.videoSamples)))
	var stcoPayload []byte
	stcoPayload = append(stcoPayload, u32(uint32(len(chunkOffs)))...)
	for _, o := range chunkOffs {
		stcoPayload = append(stcoPayload, u32(o)...)
	}
	stco := fullBox("stco", 0, 0, stcoPayload)
	stss := fullBox("stss", 0, 0, u32(1), u32(1))

	stbl := rawBox("stbl", stsd, stts, stsc, stsz, stco, stss)
	minf := rawBox("minf", vmhd, rawBox("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))), stbl)
	mdia := rawBox("mdia", mdhd, hdlr, minf)
	return rawBox("trak", tkhd, mdia)
}

func (s mp4Spec) buildAudioTrak(chunkOffs []uint32, duration uint32) []byte {
	tkhd := fullBox("tkhd", 0, 3,
		u32(0), u32(0), u32(2), u32(0), u32(duration),
		u32(0), u32(0), u16(0), u16(0), u16(0x0100), u16(0),
		matrixBytes(matrixForRotation(0)),
		u32(0), u32(0))
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(s.audioTimescale), u32(duration), u16(0x15c7), u16(0))
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte("soun"), repeat(0, 12))
	smhd := fullBox("smhd", 0, 0, u16(0), u16(0))
	mp4a := rawBox("mp4a",
		repeat(0, 6), u16(1),
		u16(0), u16(0), u16(0), u16(0),
		u16(2), u16(16), u16(0), u16(0),
		u32(s.audioTimescale<<16))

	stsd := fullBox("stsd", 0, 0, u32(1), mp4a)
	stts := fullBox("stts", 0, 0, u32(1), u32(uint32(s.audioPackets)), u32(s.audioDelta))
	stsc := fullBox("stsc", 0, 0, u32(1), u32(1), u32(1), u32(1))
	var stszPayload []byte
	stszPayload = append(stszPayload, u32(0)...)
	stszPayload = append(stszPayload, u32(uint32(s.audioPackets))...)
	for i := 0; i < s.audioPackets; i++ {
		// Payload plus the 12-byte fixed RTP header.
		stszPayload = append(stszPayload, u32(uint32(rtpPacketLen(s.audioPayload)))...)
	}
	stsz := fullBox("stsz", 0, 0, stszPayload)
	var stcoPayload []byte
	stcoPayload = append(stcoPayload, u32(uint32(len(chunkOffs)))...)
	for _, o := range chunkOffs {
		stcoPayload = append(stcoPayload, u32(o)...)
	}
	stco := fullBox("stco", 0, 0, stcoPayload)

	stbl := rawBox("stbl", stsd, stts, stsc, stsz, stco)
	minf := rawBox("minf", smhd, rawBox("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))), stbl)
	mdia := rawBox("mdia", mdhd, hdlr, minf)
	return rawBox("trak", tkhd, mdia)
}

// rtpPacketLen returns the marshalled RTP packet size for a payload size.
func rtpPacketLen(payload int) int {
	// 12-byte fixed header; the fixture never sets extensions or padding.
	return 12 + payload
}

// MdatOffsets exposes the first byte of mdat payload so tests can derive
// positions to damage.
func MdatOffsets(data []byte) (int, bool) {
	for i := 0; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) == "mdat" {
			size := int(binary.BigEndian.Uint32(data[i:]))
			if size >= 8 && i+size <= len(data) {
				return i + 8, true
			}
		}
	}
	return 0, false
}

// WriteFile writes bytes to path with 0644 permissions.
func WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
