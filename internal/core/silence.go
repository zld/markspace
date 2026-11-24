package core

import (
	"mediad/internal/model"
)

// SilenceOptions carries the threshold parameters into every report so a
// detected interval is reproducible from the stored configuration.
type SilenceOptions struct {
	// EnergyThreshold is the mean absolute sample amplitude (0..255 on the
	// synthetic PCM payloads) below which a frame counts as silent.
	EnergyThreshold float64 `json:"energy_threshold"`
	// MinDuration suppresses single-frame blips shorter than this.
	MinDuration model.PlayTime `json:"min_duration_ns"`
}

// SilenceInterval is one maximal run of silent frames.
type SilenceInterval struct {
	Start model.PlayTime `json:"start_ns"`
	End   model.PlayTime `json:"end_ns"`
	Frames int           `json:"frames"`
}

// SilenceReport is the detector output. When audio is missing or any frame
// in range is truncated, Incomplete is set and no interval is silently
// treated as non-silent evidence.
type SilenceReport struct {
	Intervals  []SilenceInterval `json:"intervals"`
	Incomplete bool              `json:"incomplete"`
	Reason     string            `json:"reason,omitempty"`
	Options    SilenceOptions    `json:"options"`
}

// frameEnergy returns the mean absolute amplitude of a frame payload.
func frameEnergy(payload []byte) float64 {
	if len(payload) == 0 {
		return 0
	}
	var sum int
	for _, b := range payload {
		// Center unsigned bytes around zero like 8-bit PCM (u-law style).
		v := int(b) - 128
		if v < 0 {
			v = -v
		}
		sum += v
	}
	return float64(sum) / float64(len(payload))
}

// DetectSilence scans one audio timeline. Missing audio and truncated
// frames produce an incomplete report rather than confident non-silence.
func DetectSilence(frames []model.Frame, kind model.TrackKind, opts SilenceOptions) SilenceReport {
	rep := SilenceReport{Options: opts}
	if kind != model.TrackAudio {
		rep.Incomplete = true
		rep.Reason = "no audio track available for silence detection"
		return rep
	}
	if len(frames) == 0 {
		rep.Incomplete = true
		rep.Reason = "audio track has no frames"
		return rep
	}
	var run *SilenceInterval
	flush := func(end model.PlayTime) {
		if run == nil {
			return
		}
		run.End = end
		if run.End-run.Start >= opts.MinDuration {
			rep.Intervals = append(rep.Intervals, *run)
		}
		run = nil
	}
	for _, f := range frames {
		if f.Truncated {
			flush(f.PTS)
			rep.Incomplete = true
			rep.Reason = "truncated audio frame cannot be classified"
			continue
		}
		if frameEnergy(f.Payload) < opts.EnergyThreshold {
			if run == nil {
				run = &SilenceInterval{Start: f.PTS, Frames: 1}
			} else {
				run.Frames++
			}
			continue
		}
		flush(f.PTS)
	}
	if run != nil {
		last := frames[len(frames)-1]
		flush(last.PTS + last.Dur)
	}
	return rep
}
