package audio

import (
	"mediad/internal/model"
)

// Resample converts a PCM frame to targetRate using linear interpolation on a
// rational sample grid. Every rounding decision is fixed, so the same frame
// always yields the same output samples.
//
// For output period i the exact source position is i*srcRate/targetRate.
// Writing it as a fraction keeps all arithmetic integer-exact:
//
//	j   = (i*srcRate) / targetRate      (source period index)
//	rem = (i*srcRate) % targetRate
//	y   = s[j] + (s[j+1]-s[j]) * rem/targetRate
//
// Boundary rule: when j reaches the last source period the last sample is
// held (edge clamp), never extrapolated. Half values round away from zero.
func Resample(in Frame, targetRate int) (Frame, ResampleStats, error) {
	if in.SampleRate <= 0 || targetRate <= 0 {
		return Frame{}, ResampleStats{}, model.New(model.ErrTimestampOverflow,
			"invalid rates src=%d dst=%d", in.SampleRate, targetRate)
	}
	if len(in.Samples) == 0 {
		return Frame{}, ResampleStats{}, model.New(model.ErrEmptyFrame, "empty frame")
	}
	if in.Channels <= 0 || len(in.Samples)%in.Channels != 0 {
		return Frame{}, ResampleStats{}, model.New(model.ErrUnsupportedLayout,
			"%d samples do not form whole %d-channel periods", len(in.Samples), in.Channels)
	}
	periods := len(in.Samples) / in.Channels
	outPeriods := periods * targetRate / in.SampleRate
	if outPeriods == 0 {
		// A source period shorter than the output grid still produces exactly
		// one output period (its first sample), deterministically.
		outPeriods = 1
	}
	out := Frame{SampleRate: targetRate, Channels: in.Channels,
		Samples: make([]int16, 0, outPeriods*in.Channels)}

	for i := 0; i < outPeriods; i++ {
		num := int64(i) * int64(in.SampleRate)
		j := num / int64(targetRate)
		rem := num % int64(targetRate)
		for ch := 0; ch < in.Channels; ch++ {
			idx := int(j)*in.Channels + ch
			v0 := int64(in.Samples[idx])
			if int(j) >= periods-1 {
				out.Samples = append(out.Samples, int16(v0)) // edge clamp
				continue
			}
			v1 := int64(in.Samples[idx+in.Channels])
			// Interpolate without floats: delta*rem/den, half away from zero.
			delta := (v1 - v0) * rem
			if delta >= 0 {
				delta = (delta + int64(targetRate)/2) / int64(targetRate)
			} else {
				delta = -((-delta + int64(targetRate)/2) / int64(targetRate))
			}
			out.Samples = append(out.Samples, int16(v0+delta))
		}
	}
	return out, ResampleStats{
		InputRate: in.SampleRate, OutputRate: targetRate,
		InputPeriods: periods, OutputPeriods: outPeriods,
	}, nil
}

// ResampleStats describes the grid conversion.
type ResampleStats struct {
	InputRate     int `json:"input_rate"`
	OutputRate    int `json:"output_rate"`
	InputPeriods  int `json:"input_periods"`
	OutputPeriods int `json:"output_periods"`
}
