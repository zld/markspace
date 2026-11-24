// Package syncer maps audio RTP clocks and declared start offsets into the
// shared media time base and enforces the one-frame A/V tolerance.
package syncer

import (
	"fmt"

	"mediarc/internal/model"
)

// Config is the declared clock mapping and video frame grid.
type Config struct {
	Mapping    model.ClockMap `json:"mapping"`
	FrameDelta uint32         `json:"frame_delta"`
}

// Alignment retains mapped and original timestamps. AlignedPTS is quantized to
// the nearest video frame only when the difference is within one frame.
type Alignment struct {
	SourceTimestamp uint32        `json:"source_timestamp"`
	MappedPTS       uint64        `json:"mapped_pts"`
	ExpectedPTS     uint64        `json:"expected_pts"`
	AlignedPTS      uint64        `json:"aligned_pts"`
	Sync            model.AVSync  `json:"sync"`
	OK              bool          `json:"ok"`
}

// Align maps one source timestamp and compares it with expectedVideoPTS.
// On failure the returned Alignment still contains both original values and
// the error has exit code 3. The caller must not overwrite source timestamps.
func Align(sourceTimestamp uint32, expectedVideoPTS uint64, cfg Config, faults *model.Collector) (Alignment, error) {
	if cfg.FrameDelta == 0 {
		cfg.FrameDelta = model.VideoFrameDelta
	}
	mapped := cfg.Mapping.Map(uint64(sourceTimestamp))
	sync := model.CheckAVSync(expectedVideoPTS, mapped, cfg.Mapping.Media, cfg.FrameDelta)
	a := Alignment{
		SourceTimestamp: sourceTimestamp,
		MappedPTS:       mapped,
		ExpectedPTS:     expectedVideoPTS,
		Sync:            sync,
	}
	if !sync.WithinFrame {
		f := model.Fault{Kind: model.FaultSync, Actual: sync.Delta.String(),
			Expected: "<=" + sync.Tolerance.String(),
			Message: "A/V drift exceeds one video frame; original timestamps retained",
			Recoverable: false}
		if faults != nil {
			faults.Add(f)
		}
		return a, model.NewExitError(model.ExitSyncConfig,
			fmt.Sprintf("audio/video sync error: drift %s > frame %s", sync.Delta, sync.FramePeriod), f)
	}
	a.OK = true
	a.AlignedPTS = ((mapped + uint64(cfg.FrameDelta)/2) / uint64(cfg.FrameDelta)) * uint64(cfg.FrameDelta)
	a.Sync.VideoPTS = a.AlignedPTS
	return a, nil
}

// ExpectedPTSForAudioSequence computes the nominal shared-time PTS for an
// RTP packet given packet index and samples per packet.
func ExpectedPTSForAudioSequence(packetIndex uint64, samplesPerPacket uint32, audioRate, videoScale uint32) uint64 {
	if audioRate == 0 {
		audioRate = model.AudioClockRate
	}
	if videoScale == 0 {
		videoScale = model.VideoTimescale
	}
	sourceTS := packetIndex * uint64(samplesPerPacket)
	return sourceTS * uint64(videoScale) / uint64(audioRate)
}
