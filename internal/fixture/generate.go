// Package fixture generates reproducible manifests, MP4 boxes and RTP traces.
package fixture

import (
	"encoding/json"
	"fmt"
	"time"

	"rtptrace/internal/manifest"
	"rtptrace/internal/timeline"
)

// Options controls deterministic fixture generation.
type Options struct {
	Seed            int64
	Duration        time.Duration
	SegmentDuration time.Duration
	Bitrates        []int
	Fault           string
	Recover         bool
}

// Fixture contains generated source data. Nothing is read from external files.
type Fixture struct {
	Options      Options
	Manifest     *manifest.Manifest
	ManifestJSON []byte
	MP4          []byte
	RTPPackets   [][]byte
}

const (
	videoID         uint32 = 1
	stereoID        uint32 = 2
	surroundID      uint32 = 3
	truncatedSample uint32 = 37
)

// Generate builds the complete seeded fixture.
func Generate(opts Options) (*Fixture, error) {
	if opts.Duration <= 0 || opts.SegmentDuration <= 0 {
		return nil, fmt.Errorf("duration and segment duration must be positive")
	}
	if opts.SegmentDuration > opts.Duration {
		return nil, fmt.Errorf("segment duration exceeds duration")
	}
	if len(opts.Bitrates) == 0 {
		opts.Bitrates = []int{800000, 1600000}
	}
	bitrates := append([]int(nil), opts.Bitrates...)
	// Stable ascending ladder order, independent of CLI enumeration order.
	sortInts(bitrates)

	segments := int(opts.Duration / opts.SegmentDuration)
	if opts.Duration%opts.SegmentDuration != 0 {
		segments++
	}
	sampleCount := int(opts.Duration / time.Second)
	if opts.Duration%time.Second != 0 {
		sampleCount++
	}

	tracks := []manifest.Track{
		videoTrack(sampleCount, opts.Fault),
		stereoAudioTrack(sampleCount),
		surroundAudioTrack(sampleCount),
	}
	deps := []manifest.Dependency{
		{TrackID: stereoID, DependsOn: videoID},
		{TrackID: surroundID, DependsOn: videoID},
	}
	m := &manifest.Manifest{
		SchemaVersion: "rtptrace.manifest/v1", Seed: opts.Seed,
		Duration: opts.Duration, SegmentDuration: opts.SegmentDuration,
		Bitrates: bitrates, Dependencies: deps, Tracks: tracks,
	}
	for _, b := range bitrates {
		m.Representations = append(m.Representations, manifest.RepresentationTarget{
			ID: manifest.BitrateID(b), Bitrate: b,
		})
	}
	applyFault(m, opts.Fault)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	mp4Bytes, err := BuildMP4(m)
	if err != nil {
		return nil, err
	}
	rtpPackets, err := GenerateRTP(opts.Seed, sampleCount)
	if err != nil {
		return nil, err
	}
	return &Fixture{Options: opts, Manifest: m, ManifestJSON: data, MP4: mp4Bytes, RTPPackets: rtpPackets}, nil
}

func videoTrack(n int, fault string) manifest.Track {
	const scale = 24000
	const start = 2 * scale
	samples := make([]manifest.Sample, 0, n)
	for i := 0; i < n; i++ {
		idx := uint32(i + 1)
		key := i%12 == 0
		size := int64(4000 + (i*137)%900)
		if key {
			size += 18000
		}
		samples = append(samples, manifest.Sample{
			Index: idx, DecodeTicks: int64(start + i*scale),
			// Movie timescale is 1000; one sample per second.
			PresentationTicks: int64(i * 1000),
			SizeBytes:         size, Sync: key,
			RTPTimestampOffset: uint32(i * 90000),
		})
	}
	t := manifest.Track{
		ID: videoID, Kind: manifest.TrackVideo, SourceTimescale: scale,
		StartTimeTicks: start, MovieTimescale: 1000, Samples: samples,
		DeclaredSampleCount: uint32(n),
		Color: &manifest.ColorMetadata{
			Primaries: "bt709", Transfer: "bt709", Matrix: "bt709",
			Source: "container", Conflict: true,
			Alternatives: []string{"bitstream:bt2020"},
		},
		EditList: videoEditList(start, n, scale),
	}
	if fault == "missing-keyframe" {
		for i := range t.Samples {
			t.Samples[i].Sync = false
		}
	}
	return t
}

func videoEditList(start int64, n, scale int) *timeline.EditList {
	return &timeline.EditList{MovieTimescale: 1000, MediaTimescale: uint32(scale), Edits: []timeline.Edit{{
		SegmentDuration: int64(n * 1000), MediaTime: start,
	}}}
}

func stereoAudioTrack(n int) manifest.Track {
	const rate = 48000
	const start = 2 * rate
	return manifest.Track{
		ID: stereoID, Kind: manifest.TrackAudio, SourceTimescale: rate,
		StartTimeTicks: start, MovieTimescale: 1000,
		Audio:   &manifest.AudioMetadata{Channels: 2, Layout: "stereo", SampleRate: rate},
		Samples: secondSamples(n, rate, start), DeclaredSampleCount: uint32(n),
	}
}

func surroundAudioTrack(n int) manifest.Track {
	const rate = 44100
	const start = 1 * rate
	return manifest.Track{
		ID: surroundID, Kind: manifest.TrackAudio, SourceTimescale: rate,
		StartTimeTicks: start, MovieTimescale: 1000,
		Audio:   &manifest.AudioMetadata{Channels: 6, Layout: "5.1", SampleRate: rate},
		Samples: secondSamples(n, rate, start), DeclaredSampleCount: uint32(n),
	}
}

func secondSamples(n, rate int, start int64) []manifest.Sample {
	out := make([]manifest.Sample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, manifest.Sample{
			Index: uint32(i + 1), DecodeTicks: start + int64(i*rate),
			PresentationTicks: int64(i * 1000), SizeBytes: 180 + int64((i*31)%40),
			Sync: true,
		})
	}
	return out
}

func applyFault(m *manifest.Manifest, fault string) {
	switch fault {
	case "", "none":
		return
	case "truncated-stbl":
		m.Fault = &manifest.FaultSpec{Kind: fault, TrackID: videoID, Sample: truncatedSample}
		for i := range m.Tracks {
			if m.Tracks[i].ID != videoID {
				continue
			}
			m.Tracks[i].DeclaredSampleCount = uint32(len(m.Tracks[i].Samples))
			m.Tracks[i].Samples = m.Tracks[i].Samples[:truncatedSample-1]
			// The damaged box/count still claims the full original ladder.
			m.Tracks[i].DeclaredSampleCount = uint32(m.Duration / time.Second)
		}
	case "edit-out-of-range":
		m.Fault = &manifest.FaultSpec{Kind: fault, TrackID: videoID, Recoverable: true}
		for i := range m.Tracks {
			if m.Tracks[i].ID == videoID && m.Tracks[i].EditList != nil {
				m.Tracks[i].EditList.Edits[0].SegmentDuration += 2000
			}
		}
	case "invalid-dependency":
		m.Fault = &manifest.FaultSpec{Kind: fault, TrackID: 999}
		m.Dependencies = append(m.Dependencies, manifest.Dependency{TrackID: videoID, DependsOn: 999})
	case "missing-keyframe":
		m.Fault = &manifest.FaultSpec{Kind: fault, TrackID: videoID, Sample: 7}
	default:
		// Unknown faults are rejected by the command before generation.
		m.Fault = &manifest.FaultSpec{Kind: fault}
	}
}

func sortInts(xs []int) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
