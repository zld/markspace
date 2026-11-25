package fixture

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteAsset creates a deterministic MP4 file with the requested fixture properties.
func WriteAsset(path string, raw Spec) error {
	s := raw.withDefaults()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(ftypBox()); err != nil {
		return err
	}
	mdatStart, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	payloadStart := mdatStart + 8

	videoSizes := make([]uint32, s.VideoSamples)
	var videoBytes int64
	for i := range videoSizes {
		videoSizes[i] = videoSampleSize(i, isKeyframe(i, s.KeyframeEvery))
		videoBytes += int64(videoSizes[i])
	}
	audioCount := 0
	var audioBytes int64
	if s.IncludeAudio {
		audioCount = s.AudioSamples
		audioBytes = int64(audioCount) * 512
	}
	if _, err := f.Write(boxHeader("mdat", 8+videoBytes+audioBytes)); err != nil {
		return err
	}

	videoOffsets := make([]uint32, s.VideoSamples)
	cursor := payloadStart
	var zeros [4096]byte
	for i, sz := range videoSizes {
		videoOffsets[i] = uint32(cursor)
		if err := writeZeros(f, int64(sz), zeros[:]); err != nil {
			return err
		}
		cursor += int64(sz)
	}
	audioOffsets := make([]uint32, audioCount)
	for i := 0; i < audioCount; i++ {
		audioOffsets[i] = uint32(cursor)
		if err := writeZeros(f, 512, zeros[:]); err != nil {
			return err
		}
		cursor += 512
	}

	moov := buildMoov(s, videoSizes, videoOffsets, audioOffsets)
	if _, err := f.Write(moov); err != nil {
		return err
	}
	return f.Close()
}

func writeZeros(w io.Writer, n int64, buf []byte) error {
	for n > 0 {
		k := int64(len(buf))
		if n < k {
			k = n
		}
		if _, err := w.Write(buf[:k]); err != nil {
			return err
		}
		n -= k
	}
	return nil
}

func buildMoov(s Spec, videoSizes, videoOffsets, audioOffsets []uint32) []byte {
	durationMS := uint32(s.DurationMS())
	videoDuration := uint32(s.VideoSamples) * s.SampleDelta
	videoStbl := []byte{}
	videoStbl = append(videoStbl, stsdBox(videoSampleEntry(s.Width, s.Height))...)
	videoStbl = append(videoStbl, sttsBox(uint32(s.VideoSamples), s.SampleDelta)...)
	if s.CompositionOffset != 0 {
		videoStbl = append(videoStbl, cttsBox(s.CompositionOffset, s.VideoSamples)...)
	}
	videoStbl = append(videoStbl, stssBox(s)...)
	videoStbl = append(videoStbl, stscBox()...)
	videoStbl = append(videoStbl, stszBox(videoSizes)...)
	videoStbl = append(videoStbl, stcoBox(videoOffsets)...)
	videoMinf := append(box("vmhd", append(fullHeader(0, 1), make([]byte, 8)...)), box("stbl", videoStbl)...)
	videoMdia := append(mdhdBox(s.VideoTimescale, videoDuration), hdlrBox("vide", "VideoHandler")...)
	videoMdia = append(videoMdia, box("minf", videoMinf)...)
	videoTrak := tkhdBox(1, durationMS, 0, s.Width, s.Height)
	if s.OOBEdit {
		videoTrak = append(videoTrak, box("edts", elstOOBBox(s, durationMS))...)
	}
	videoTrak = append(videoTrak, box("mdia", videoMdia)...)

	moovPayload := mvhdBox(durationMS)
	moovPayload = append(moovPayload, box("trak", videoTrak)...)
	if s.IncludeAudio {
		audioN := s.AudioSamples
		audioSizes := make([]uint32, audioN)
		for i := range audioSizes {
			audioSizes[i] = 512
		}
		audioStbl := stsdBox(audioSampleEntry())
		audioStbl = append(audioStbl, sttsBox(uint32(audioN), 1024)...)
		audioStbl = append(audioStbl, stscBox()...)
		audioStbl = append(audioStbl, stszBox(audioSizes)...)
		audioStbl = append(audioStbl, stcoBox(audioOffsets)...)
		audioMinf := append(box("smhd", append(fullHeader(0, 0), make([]byte, 4)...)), box("stbl", audioStbl)...)
		audioMdia := append(mdhdBox(48000, uint32(audioN*1024)), hdlrBox("soun", "SoundHandler")...)
		audioMdia = append(audioMdia, box("minf", audioMinf)...)
		audioTrak := tkhdBox(2, durationMS, 0x0100, 0, 0)
		audioTrak = append(audioTrak, box("mdia", audioMdia)...)
		moovPayload = append(moovPayload, box("trak", audioTrak)...)
	}
	return box("moov", moovPayload)
}

// CorruptBoxLength overwrites the first box size with an impossibly short value.
func CorruptBoxLength(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteAt(be32(5), 0)
	return err
}

// TruncateBox cuts the file inside its first mdat payload.
func TruncateBox(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() < 64 {
		return fmt.Errorf("fixture too small to truncate safely")
	}
	return os.Truncate(path, 48)
}
