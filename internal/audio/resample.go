package audio

import (
	"fmt"
	"math"

	"mediacore/internal/timeline"
)

// Resampler converts between sample rates with a fixed windowed-sinc kernel.
// Filter state is per contiguous segment. The reset policy is explicit and
// deliberate: every segment starts with zeroed history, because carrying
// history across a gap would smear audio that was never adjacent. Each reset
// is counted so the quality report shows the cost.
type Resampler struct {
	HalfTaps int // taps on each side of the centre; total taps = 2*HalfTaps
	Cutoff   float64
}

// NewResampler returns a resampler with a conservative default kernel.
func NewResampler() Resampler { return Resampler{HalfTaps: 16, Cutoff: 0.94} }

// ResampleStats reports what conversion did, including the parts that are
// usually hidden: segment resets and the zero-padded tail.
type ResampleStats struct {
	InputFrames  int
	OutputFrames int
	Resets       int
	TailFrames   int // output frames produced from the zero-padded tail
	Segments     int
}

// ResampleAt converts one contiguous buffer. The input rate is explicit
// because a Buffer carries only its layout, never a time base. Output frames
// are produced on an exact rational grid from the timeline cursor, and the
// filter history starts at zero for every call (one counted reset).
func (r Resampler) ResampleAt(src *Buffer, inRate, outRate int) (*Buffer, ResampleStats, error) {
	if err := src.Validate(); err != nil {
		return nil, ResampleStats{}, err
	}
	if inRate <= 0 || outRate <= 0 {
		return nil, ResampleStats{}, fmt.Errorf("audio: invalid resample rates %d -> %d", inRate, outRate)
	}
	if r.HalfTaps <= 0 || r.HalfTaps > 128 {
		return nil, ResampleStats{}, fmt.Errorf("audio: invalid kernel size %d", r.HalfTaps)
	}
	stats := ResampleStats{InputFrames: src.Frames, Segments: 1}
	if inRate == outRate {
		out := src.Clone()
		stats.OutputFrames = out.Frames
		return out, stats, nil
	}

	n, err := timeline.CursorCount(int64(src.Frames), int64(inRate), int64(outRate))
	if err != nil {
		return nil, stats, err
	}
	// The cursor rounds the last output frame nearest the final input sample;
	// when it sits beyond the half-tap support, the final contribution is
	// windowed from zero padding. Count that flushed tail explicitly.
	if n > 0 {
		last, _ := timeline.CursorCount(int64(src.Frames-1), int64(inRate), int64(outRate))
		if last < n {
			stats.TailFrames = 1
		}
	}
	out, err := NewBuffer(src.Layout, int(n))
	if err != nil {
		return nil, stats, err
	}
	out.Start = outStartFor(src.Start, inRate, outRate)
	out.Metadata = src.Metadata
	out.Metadata.ResetCount++ // state starts at zero: count the reset
	stats.Resets = 1
	stats.OutputFrames = out.Frames

	ch := src.Layout.NumChannels()
	ratio := float64(outRate) / float64(inRate)
	cutoff := r.Cutoff
	if ratio < 1 {
		cutoff *= ratio
	}
	if cutoff <= 0 || cutoff > 1 {
		cutoff = 1
	}
	weights := make([]float64, 2*r.HalfTaps)
	cursor, err := timeline.NewSampleCursor(int64(inRate), int64(outRate), 0, 0, int64(out.Frames))
	if err != nil {
		return nil, stats, err
	}
	for i := int64(0); i < int64(out.Frames); i++ {
		ph, err := cursor.At(i)
		if err != nil {
			return nil, stats, err
		}
		frac := float64(ph.Remainder) / float64(outRate)
		sum := 0.0
		for k := 0; k < 2*r.HalfTaps; k++ {
			t := float64(k-r.HalfTaps+1) - frac
			w := sinc(t*cutoff) * blackman((float64(k-r.HalfTaps+1)-frac)/float64(r.HalfTaps))
			weights[k] = w
			sum += w
		}
		if sum == 0 {
			sum = 1
		}
		frame := out.Frame(int(i))
		for c := 0; c < ch; c++ {
			acc := 0.0
			for k := 0; k < 2*r.HalfTaps; k++ {
				idx := ph.Base + int64(k-r.HalfTaps+1)
				if idx < 0 || idx >= int64(src.Frames) {
					continue // zero padding at both ends
				}
				acc += src.Data[int(idx)*ch+c] * weights[k]
			}
			frame[c] = acc / sum
		}
	}
	return out, stats, nil
}

// outStartFor maps a source-grid start onto the output grid using the same
// rounding rule as every other conversion.
func outStartFor(srcStart int64, inRate, outRate int) int64 {
	v, err := timeline.Convert(timeline.Ticks(srcStart), timeline.MustScale(int64(inRate)), timeline.MustScale(int64(outRate)))
	if err != nil {
		return 0
	}
	return int64(v)
}

// sinc is the normalized sinc function.
func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

// blackman is a Blackman window over [-1,1]; outside that range it is zero.
func blackman(x float64) float64 {
	if x <= -1 || x >= 1 {
		return 0
	}
	const a0, a1, a2 = 0.42, 0.5, 0.08
	t := math.Pi * (x + 1)
	return a0 - a1*math.Cos(t) + a2*math.Cos(2*t)
}
