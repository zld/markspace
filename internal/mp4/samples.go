package mp4mux

import (
	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/syncer"
)

// buildSamples rebuckets local ticks and derives each duration from the next
// PTS. The final sample reuses the previous duration, which keeps total
// duration equal to the frame span rather than inventing a tail.
func buildSamples(rep *syncer.Report, tl *clock.Timeline, cfg Config) ([]Sample, error) {
	first := rep.Video[0].LocalTick
	out := make([]Sample, len(rep.Video))
	for i, vf := range rep.Video {
		local := vf.LocalTick - first
		if local < 0 {
			return nil, fault.New(fault.StageMux, fault.KindTimestamp,
				"sample %d local tick %d is negative", i, local)
		}
		out[i] = Sample{
			Record:     vf.Record,
			PTS:        uint64(tl.Rebucket(local, cfg.Timescale)),
			DTS:        uint64(tl.Rebucket(local, cfg.Timescale)),
			Keyframe:   vf.Record.Keyframe(),
			SourceOff:  vf.Record.Offset,
			SourceHash: vf.Record.Meta.Checksum,
		}
	}
	defaultDur := uint32(clock.ScaleTicks(3000, 90000, cfg.Timescale))
	if defaultDur == 0 {
		defaultDur = 1
	}
	for i := range out {
		if i+1 < len(out) {
			d := out[i+1].PTS - out[i].PTS
			if d == 0 || d > uint64(^uint32(0)) {
				d = uint64(defaultDur)
			}
			out[i].Duration = uint32(d)
			continue
		}
		if i > 0 {
			out[i].Duration = out[i-1].Duration
		} else {
			out[i].Duration = defaultDur
		}
	}
	return out, nil
}

// buildAudio converts recovered units into encoded audio samples. Concealment
// slots carry no bytes; writeAudioTrak folds their duration into a neighbour.
func buildAudio(rep *syncer.Report, tl *clock.Timeline, cfg Config) ([]AudioSample, error) {
	if len(rep.Audio) == 0 {
		return nil, nil
	}
	first := rep.Audio[0].LocalTick
	out := make([]AudioSample, 0, len(rep.Audio))
	for i, slot := range rep.Audio {
		local := slot.LocalTick - first
		if local < 0 {
			return nil, fault.New(fault.StageMux, fault.KindTimestamp,
				"audio slot %d local tick %d is negative", i, local)
		}
		var data []byte
		if slot.Unit.Packet != nil {
			data = slot.Unit.Packet.Payload
		}
		dur := uint32(clock.ScaleTicks(slot.DurationTicks, tl.Timescale, cfg.AudioTimescale))
		if dur == 0 {
			dur = cfg.AudioTimescale / 50
			if dur == 0 {
				dur = 1
			}
		}
		out = append(out, AudioSample{
			Data:     data,
			Duration: dur,
			PTS:      uint64(clock.ScaleTicks(local, tl.Timescale, cfg.AudioTimescale)),
			Silence:  slot.Unit.Silence,
		})
	}
	return out, nil
}
