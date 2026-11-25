// Package audio detects silence ranges on the unified nanosecond timeline.
package audio

import (
	"encoding/binary"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/trace"
)

// Config controls silence detection. A frame is silent when its absolute
// amplitude is <= ThresholdAmplitude.
type Config struct {
	ThresholdAmplitude int      `json:"threshold_amplitude"`
	MinDurationNS      model.NS `json:"min_duration_ns"`
	SampleRate         uint32   `json:"sample_rate,omitempty"`
}

// DefaultConfig returns a conservative fixture-friendly configuration.
func DefaultConfig() Config {
	return Config{ThresholdAmplitude: 500, MinDurationNS: 150 * model.NSPerSecond / 1000}
}

// Interval is a detected silence range [Start, End).
type Interval struct {
	TrackID int `json:"track_id"`
	model.Interval
	StartFrameTick uint64 `json:"start_frame_tick"`
	EndFrameTick   uint64 `json:"end_frame_tick"`
}

// DetectTrack analyzes little-endian mono int16 payloads. Track timescale is
// interpreted as the PCM frame rate when Config.SampleRate is zero.
func DetectTrack(tr *model.Track, cfg Config) ([]Interval, error) {
	if tr == nil {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageAudio, "nil audio track")
	}
	if cfg.ThresholdAmplitude < 0 {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageAudio, "threshold must be non-negative").WithTrack(tr.ID)
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = tr.TimeScale
	}
	if rate == 0 {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageAudio, "audio sample rate/timescale is zero").WithTrack(tr.ID)
	}

	var (
		results   []Interval
		runStart  *uint64
		lastTick  uint64
		haveFrame bool
	)

	closeRun := func(endTick uint64) error {
		if runStart == nil {
			return nil
		}
		startNS, err := model.TicksToNS(*runStart, rate)
		if err != nil {
			return trace.Wrap(trace.StageAudio, err).(*trace.Error).WithTrack(tr.ID)
		}
		endNS, err := model.TicksToNS(endTick, rate)
		if err != nil {
			return trace.Wrap(trace.StageAudio, err).(*trace.Error).WithTrack(tr.ID)
		}
		interval := model.Interval{Start: tr.OffsetNS + startNS, End: tr.OffsetNS + endNS}
		if interval.Length() >= cfg.MinDurationNS {
			results = append(results, Interval{
				TrackID: tr.ID, Interval: interval,
				StartFrameTick: *runStart, EndFrameTick: endTick,
			})
		}
		runStart = nil
		return nil
	}

	for _, s := range tr.Samples {
		if len(s.Data)%2 != 0 {
			return nil, trace.E(trace.CodeInvalidArgument, trace.StageAudio, "odd PCM byte count %d", len(s.Data)).
				WithTrack(tr.ID).WithOffset(s.PayloadOffset)
		}
		frames := len(s.Data) / 2
		for f := 0; f < frames; f++ {
			raw := binary.LittleEndian.Uint16(s.Data[f*2 : f*2+2])
			amp := int32(int16(raw))
			if amp < 0 {
				amp = -amp
			}
			tick := s.StartTick + uint64(f)
			if amp <= int32(cfg.ThresholdAmplitude) {
				if runStart == nil {
					v := tick
					runStart = &v
				}
			} else if err := closeRun(tick); err != nil {
				return nil, err
			}
			lastTick = tick + 1
			haveFrame = true
		}
	}
	if haveFrame {
		if err := closeRun(lastTick); err != nil {
			return nil, err
		}
	}
	return results, nil
}
