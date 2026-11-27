package audio

import (
	"medialib/internal/clock"
	merr "medialib/internal/errors"
)

// Resample converts in to outRate with linear interpolation. The read
// position advances as an exact rational (inRate/outRate per output
// frame); only the local interpolation fraction is floating point, so
// no timing error accumulates across the stream.
func Resample(in PCM, outRate int) (PCM, error) {
	if in.Rate <= 0 || in.Channels <= 0 {
		return PCM{}, merr.Newf(merr.StageResample, -1, "rate",
			"invalid input rate=%d ch=%d", in.Rate, in.Channels)
	}
	if outRate <= 0 {
		return PCM{}, merr.Newf(merr.StageResample, -1, "rate",
			"invalid output rate %d", outRate)
	}
	if in.Rate == outRate {
		return in, nil
	}
	inFrames := in.Frames()
	if inFrames == 0 {
		return PCM{}, merr.Newf(merr.StageResample, -1, "rate", "empty input")
	}
	tl, err := clock.NewTimeline(int64(in.Rate))
	if err != nil {
		return PCM{}, merr.New(merr.StageResample, -1, "rate", err)
	}
	// Output frame count from the exact input duration, rounded to
	// nearest output tick.
	dur := tl.Duration(int64(inFrames))
	outTL, err := clock.NewTimeline(int64(outRate))
	if err != nil {
		return PCM{}, merr.New(merr.StageResample, -1, "rate", err)
	}
	outFrames64, _, err := outTL.Quantize(dur)
	if err != nil {
		return PCM{}, merr.New(merr.StageResample, -1, "rate", err)
	}
	outFrames := int(outFrames64)
	out := PCM{Rate: outRate, Channels: in.Channels,
		Samples: make([]float32, outFrames*in.Channels)}
	step := clock.Must(int64(in.Rate), int64(outRate)) // input seconds per output frame
	pos := clock.Must(0, 1)
	for f := 0; f < outFrames; f++ {
		idx := pos.Num / pos.Den
		frac := float64(pos.Num%pos.Den) / float64(pos.Den)
		i0 := int(idx)
		i1 := i0 + 1
		if i1 >= inFrames {
			i1 = inFrames - 1
		}
		for c := 0; c < in.Channels; c++ {
			a := in.Samples[i0*in.Channels+c]
			b := in.Samples[i1*in.Channels+c]
			out.Samples[f*in.Channels+c] = a + float32(frac)*(b-a)
		}
		pos, err = pos.Add(step)
		if err != nil {
			return PCM{}, merr.New(merr.StageResample, int64(f), "advance", err)
		}
	}
	return out, nil
}
