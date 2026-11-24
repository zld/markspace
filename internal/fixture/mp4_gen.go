package fixture

import (
	"bytes"
	"encoding/binary"
)

// MP4Spec configures a generated MP4 sample. All numeric choices are
// explicit so the analyzer's ground truth is fully known.
type MP4Spec struct {
	Seed           int64
	VideoFrames    int
	KeyEvery       int // keyframe interval in frames; frame 0 always key
	Width          uint32
	Height         uint32
	Rotation       int    // 0/90/180/270 degrees in the tkhd matrix
	ColorPrim      uint16 // nclx colour primaries
	VideoTimescale uint32 // 0 -> 30000
	AudioTimescale uint32 // 0 -> 48000
}

// DefaultMP4Spec returns the spec used by the demo and most tests.
func DefaultMP4Spec(seed int64) MP4Spec {
	return MP4Spec{
		Seed: seed, VideoFrames: 90, KeyEvery: 15,
		Width: 640, Height: 360, ColorPrim: 1,
		VideoTimescale: 30000, AudioTimescale: 48000,
	}
}

const (
	mp4VideoSample   = 120
	mp4AudioSample   = 80
	mp4AudioPerVideo = 2 // audio samples per 33.33ms video frame
)

// GenerateMP4 builds a valid ftyp|moov|mdat file (moov first) and returns
// the bytes plus a manifest of every sample's offset, time and keyframe
// flag. The analyzer must reproduce the manifest from the bytes alone.
func GenerateMP4(spec MP4Spec) ([]byte, MP4Manifest) {
	if spec.VideoTimescale == 0 {
		spec.VideoTimescale = 30000
	}
	if spec.AudioTimescale == 0 {
		spec.AudioTimescale = 48000
	}
	rng := NewRand(spec.Seed)

	var vid, aud []mp4Sample
	for i := 0; i < spec.VideoFrames; i++ {
		vid = append(vid, mp4Sample{
			trackID:   1,
			size:      mp4VideoSample,
			key:       i%spec.KeyEvery == 0,
			scaleTick: int64(i) * int64(spec.VideoTimescale) / 30,
		})
	}
	audioCount := spec.VideoFrames * mp4AudioPerVideo
	audioDelta := int64(spec.AudioTimescale) / 30 / mp4AudioPerVideo
	for i := 0; i < audioCount; i++ {
		aud = append(aud, mp4Sample{
			trackID:   2,
			size:      mp4AudioSample,
			scaleTick: int64(i) * audioDelta,
		})
	}

	// Build mdat payload; keyframes get a structural marker byte.
	var mdat bytes.Buffer
	putSample := func(s mp4Sample) {
		buf := make([]byte, s.size)
		rng.Fill(buf)
		if s.key {
			buf[0] = 0x67
		}
		mdat.Write(buf)
	}
	for _, s := range vid {
		putSample(s)
	}
	for _, s := range aud {
		putSample(s)
	}

	// First moov pass with placeholder offsets; its size is final because
	// the stco entry count is fixed at one per track.
	probe := buildMoov(spec, vid, aud, 0, 0)
	var ftyp bytes.Buffer
	mp4Box(&ftyp, "ftyp", ftypPayload())
	videoChunk := uint32(ftyp.Len() + len(probe) + 8)
	audioChunk := videoChunk + uint32(len(vid))*mp4VideoSample
	moov := buildMoov(spec, vid, aud, videoChunk, audioChunk)

	manifest := MP4Manifest{
		VideoTimescale: spec.VideoTimescale, AudioTimescale: spec.AudioTimescale,
		Width: spec.Width, Height: spec.Height,
		Rotation: spec.Rotation, ColorPrimaries: spec.ColorPrim,
	}
	cursor := int64(videoChunk)
	record := func(s mp4Sample, idx uint32) SampleDesc {
		d := SampleDesc{Track: s.trackID, Index: idx, Offset: cursor,
			Size: s.size, Keyframe: s.key, Complete: true}
		if s.trackID == 1 {
			d.TimeTick = s.scaleTick * 48000 / int64(spec.VideoTimescale)
		} else {
			d.TimeTick = s.scaleTick * 48000 / int64(spec.AudioTimescale)
		}
		cursor += int64(s.size)
		return d
	}
	for i, s := range vid {
		manifest.Samples = append(manifest.Samples, record(s, uint32(i+1)))
	}
	for i, s := range aud {
		manifest.Samples = append(manifest.Samples, record(s, uint32(i+1)))
	}

	var out bytes.Buffer
	out.Write(ftyp.Bytes())
	out.Write(moov)
	mp4Box(&out, "mdat", mdat.Bytes())
	return out.Bytes(), manifest
}

func ftypPayload() []byte {
	var b bytes.Buffer
	b.WriteString("isom")
	_ = binary.Write(&b, binary.BigEndian, uint32(0x200))
	b.WriteString("isomiso2avc1mp41")
	return b.Bytes()
}
