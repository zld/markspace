package output

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	gomp4 "github.com/abema/go-mp4"

	"medcheck/internal/adapter"
	"medcheck/internal/clock"
)

// Sample is one encoded media sample.
type Sample struct {
	PTSNS      int64
	DurationNS int64
	Payload    []byte
	Keyframe   bool
}

// Document is the deterministic MP4 document written by medcheck.
type Document struct {
	AudioSamples []Sample
	Audio        adapter.AudioSpec
	VideoSamples []Sample
	Video        adapter.VideoSpec
}

// Build serializes an MP4 and returns its SHA-256.
func Build(doc Document) ([]byte, string, error) {
	normalizeDoc(&doc)
	ftyp := ftypBox()
	dataOffset := len(ftyp) + 8

	var mdatPayload []byte
	audioOffset := uint32(dataOffset)
	audioSizes := sizes(doc.AudioSamples)
	for _, s := range doc.AudioSamples {
		mdatPayload = append(mdatPayload, s.Payload...)
	}
	videoOffset := uint32(dataOffset + len(mdatPayload))
	videoSizes := sizes(doc.VideoSamples)
	for _, s := range doc.VideoSamples {
		mdatPayload = append(mdatPayload, s.Payload...)
	}
	mdat := box("mdat", mdatPayload)

	durationMS := documentDurationMS(doc)
	var traks []byte
	trackID := uint32(1)
	if len(doc.AudioSamples) > 0 {
		trak, err := trakBox(trackID, clock.StreamAudio, uint32(doc.Audio.SampleRate), doc.AudioSamples,
			audioSizes, audioSampleEntry(doc.Audio), audioOffset, durationMS, doc)
		if err != nil {
			return nil, "", err
		}
		traks = append(traks, trak...)
		trackID++
	}
	if len(doc.VideoSamples) > 0 {
		trak, err := trakBox(trackID, clock.StreamVideo, 90000, doc.VideoSamples,
			videoSizes, videoSampleEntry(doc.Video), videoOffset, durationMS, doc)
		if err != nil {
			return nil, "", err
		}
		traks = append(traks, trak...)
	}
	meta, err := metadataBox(doc)
	if err != nil {
		return nil, "", err
	}
	moov := box("moov", appendBytes(mvhdBox(durationMS), meta, traks))
	out := append(append(append([]byte{}, ftyp...), mdat...), moov...)
	sum := sha256.Sum256(out)
	return out, hex.EncodeToString(sum[:]), nil
}

// WriteFile builds, validates, and writes an MP4 document.
func WriteFile(path string, doc Document) (string, int, error) {
	data, sum, err := Build(doc)
	if err != nil {
		return "", 0, err
	}
	if _, err := Validate(data); err != nil {
		return "", 0, err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", 0, err
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", 0, err
	}
	return sum, len(data), nil
}

// Validate parses container structure with go-mp4.
func Validate(data []byte) (*gomp4.ProbeInfo, error) {
	info, err := gomp4.Probe(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("output: mp4 probe failed: %w", err)
	}
	if _, err := gomp4.ReadBoxStructure(bytes.NewReader(data), func(h *gomp4.ReadHandle) (interface{}, error) {
		return nil, nil
	}); err != nil {
		return nil, fmt.Errorf("output: mp4 box structure invalid: %w", err)
	}
	return info, nil
}

// SHA256 returns the hex checksum of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeDoc(doc *Document) {
	if doc.Audio.Version == "" {
		doc.Audio = adapter.AudioSpec{
			Version: adapter.VersionAACV1, Codec: "AAC", Channels: 2, Layout: "stereo",
			SampleRate: 48000, ChannelMap: []string{"FL", "FR"},
		}
	}
	if doc.Video.Version == "" {
		doc.Video = adapter.VideoSpec{Version: adapter.VersionH264V1, Codec: "H.264"}
	}
}

func sizes(samples []Sample) []uint32 {
	out := make([]uint32, len(samples))
	for i, s := range samples {
		out[i] = uint32(len(s.Payload))
	}
	return out
}

func documentDurationMS(doc Document) uint32 {
	var maxNS int64
	for _, list := range [][]Sample{doc.AudioSamples, doc.VideoSamples} {
		for _, s := range list {
			if end := s.PTSNS + s.DurationNS; end > maxNS {
				maxNS = end
			}
		}
	}
	return uint32((maxNS + 999_999) / 1_000_000)
}
