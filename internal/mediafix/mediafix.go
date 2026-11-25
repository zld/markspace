// Package mediafix generates deterministic in-repository test media fixtures.
package mediafix

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/maptrack"
	"mediacore/internal/remux"
	"mediacore/internal/scan"
)

const (
	DurationNS     int64  = 2 * clock.NSPerSecond
	VideoTimescale uint32 = 24000
	VideoFrames    uint32 = 48
	VideoDelta     uint32 = 1000
	VideoFrameSize int    = 20_000
	AudioTimescale uint32 = 48000
	AudioBlocks    uint32 = 100
	AudioDelta     uint32 = 960
	AudioBlockSize int    = 3840
	TargetBitrate  int64  = 5_376_000
)

// MasterIndex describes the generated two-track master.
func MasterIndex(marker byte) *scan.Index {
	p := scan.DefaultParams()
	p.TargetBitrate = TargetBitrate
	return &scan.Index{
		Version:      scan.IndexVersion,
		SourcePath:   "",
		Format:       scan.FormatMP4,
		SourceSHA256: fmt.Sprintf("generated-master-%02x", marker),
		Params:       p,
		Tracks: []scan.Track{
			{ID: 1, Handler: "vide", Kind: "video", Codec: "mp4v", Timescale: VideoTimescale, DurationUnits: uint64(VideoFrames * VideoDelta), DurationNS: DurationNS, Width: 320, Height: 240},
			{ID: 2, Handler: "soun", Kind: "audio", Codec: "mp4a", Timescale: AudioTimescale, DurationUnits: uint64(AudioBlocks * AudioDelta), DurationNS: DurationNS, Channels: 2, BitsPerSample: 16, SampleRate: 48000},
		},
	}
}

// MasterSamples returns deterministic, exactly-sized lossless samples.
func MasterSamples(marker byte) []scan.SampleRef {
	out := make([]scan.SampleRef, 0, VideoFrames+AudioBlocks)
	vTB := clock.Timebase{Timescale: VideoTimescale}
	for i := uint32(0); i < VideoFrames; i++ {
		data := make([]byte, VideoFrameSize)
		data[0] = marker
		binary.BigEndian.PutUint32(data[1:5], i)
		for j := 5; j < len(data); j++ {
			data[j] = byte((int(i)*31 + j*7) & 0xff)
		}
		dts := int64(i) * int64(VideoDelta)
		out = append(out, scan.SampleRef{
			TrackID: 1, Index: i, Size: uint32(len(data)), Data: data,
			PTSUnits: dts, DTSUnits: dts, DurationUnits: VideoDelta,
			PTSNS: vTB.ToNS(dts), DTSNS: vTB.ToNS(dts), KeyFrame: true,
		})
	}
	aTB := clock.Timebase{Timescale: AudioTimescale}
	for i := uint32(0); i < AudioBlocks; i++ {
		data := make([]byte, AudioBlockSize)
		data[0] = marker ^ 0xff
		binary.BigEndian.PutUint32(data[1:5], i)
		for j := 4; j < len(data); j++ {
			// Deterministic nonzero 16-bit PCM-like pattern.
			data[j] = byte((j*13 + int(i)*17) & 0xff)
		}
		dts := int64(i) * int64(AudioDelta)
		out = append(out, scan.SampleRef{
			TrackID: 2, Index: i, Size: uint32(len(data)), Data: data,
			PTSUnits: dts, DTSUnits: dts, DurationUnits: AudioDelta,
			PTSNS: aTB.ToNS(dts), DTSNS: aTB.ToNS(dts), KeyFrame: true,
		})
	}
	return out
}

// WriteMaster creates a valid MP4 fixture and returns its path.
func WriteMaster(path string, marker byte) (*remux.Result, error) {
	idx := MasterIndex(marker)
	samples := MasterSamples(marker)
	mappings, err := maptrack.Map(idx)
	if err != nil {
		return nil, err
	}
	return remux.WriteMP4(idx, mappings, samples, remux.Options{
		OutputPath: path, AllowedRoot: filepath.Dir(path), Resume: true,
	})
}
