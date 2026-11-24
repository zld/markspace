package audio

import (
	"math"

	"mediad/internal/mederr"
)

// Resample converts a planar buffer to outRate using stateless linear
// interpolation. Every output frame is mapped directly from source frame
// zero by the rational ratio inRate/outRate; there is no carried phase and
// therefore no cumulative error at chunk or edit boundaries.
func Resample(in *Buffer, outRate int) (*Buffer, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if outRate <= 0 {
		return nil, mederr.New(mederr.CatAudio, "audio", -1, -1, false, "output rate must be positive")
	}
	if in.Rate == outRate {
		out := in.Clone()
		return out, nil
	}
	inFrames := int64(in.Frames())
	// Output duration is preserved by rounding the output frame count.
	outFrames := int(math.Round(float64(inFrames) * float64(outRate) / float64(in.Rate)))
	if outFrames == 0 && inFrames > 0 {
		outFrames = 1
	}
	out, err := NewBuffer(outRate, in.Layout, outFrames)
	if err != nil {
		return nil, err
	}
	numRate := int64(in.Rate)
	denRate := int64(outRate)
	for j := int64(0); j < int64(outFrames); j++ {
		pos := j * numRate
		i0 := pos / denRate
		fracNum := pos - i0*denRate
		for c := 0; c < in.Layout.Channels; c++ {
			v := interpolate(in.Planar[c], i0, fracNum, denRate, inFrames)
			out.Planar[c][j] = v
		}
	}
	return out, nil
}

// ResampleRegion renders frames frames starting at sourceStartNs without
// carrying resampler state between regions. It is the chunk-safe entry point:
// source coordinates are derived from the unified nanosecond timeline.
func ResampleRegion(in *Buffer, sourceStartNs int64, frames, outRate int) (*Buffer, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if frames < 0 || outRate <= 0 {
		return nil, mederr.New(mederr.CatAudio, "audio", sourceStartNs, -1, false,
			"invalid region frames=%d rate=%d", frames, outRate)
	}
	out, err := NewBuffer(outRate, in.Layout, frames)
	if err != nil {
		return nil, err
	}
	if frames == 0 {
		return out, nil
	}
	// Map each output frame directly to absolute source time. Floating
	// point is used only for the short interpolation fraction; edit and
	// chunk coordinates stay exact integer nanoseconds before this point.
	startSeconds := float64(sourceStartNs) / 1e9
	for j := 0; j < frames; j++ {
		pos := (startSeconds + float64(j)/float64(outRate)) * float64(in.Rate)
		i0 := int64(math.Floor(pos))
		frac := pos - float64(i0)
		for c := 0; c < in.Layout.Channels; c++ {
			v := interpolateFraction(in.Planar[c], i0, frac, int64(in.Frames()))
			out.Planar[c][j] = v
		}
	}
	return out, nil
}

func interpolate(ch []float64, i0, fracNum, den, frames int64) float64 {
	if den <= 0 {
		return 0
	}
	return interpolateFraction(ch, i0, float64(fracNum)/float64(den), frames)
}

func interpolateFraction(ch []float64, i0 int64, frac float64, frames int64) float64 {
	if i0 < 0 {
		return 0
	}
	if i0 >= frames-1 {
		if i0 >= frames {
			return 0
		}
		return ch[i0]
	}
	a := ch[i0]
	b := ch[i0+1]
	return a + (b-a)*frac
}

// Convert performs rate conversion followed by layout normalization.
func Convert(in *Buffer, targetRate int, target Layout) (*Buffer, error) {
	rs, err := Resample(in, targetRate)
	if err != nil {
		return nil, err
	}
	return ConvertLayout(rs, target)
}
