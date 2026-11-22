package audio

import (
	"fmt"
	"math"
)

// Stats is the quality accounting every stage folds into. It is additive:
// callers accumulate per segment and the totals stay meaningful across gaps,
// resets, and layout changes.
type Stats struct {
	InputFrames    int
	OutputFrames   int
	ClippedSamples int
	SilentSamples  int
	Resets         int
	TailFrames     int
	Segments       int
	MaxAbsSample   float64
	SumSquares     float64
}

// Add merges another stats value.
func (s *Stats) Add(o Stats) {
	s.InputFrames += o.InputFrames
	s.OutputFrames += o.OutputFrames
	s.ClippedSamples += o.ClippedSamples
	s.SilentSamples += o.SilentSamples
	s.Resets += o.Resets
	s.TailFrames += o.TailFrames
	s.Segments += o.Segments
	if o.MaxAbsSample > s.MaxAbsSample {
		s.MaxAbsSample = o.MaxAbsSample
	}
	s.SumSquares += o.SumSquares
}

// RMSDBFS is the RMS level, floored so an all-silent buffer still reports a
// finite number.
func (s Stats) RMSDBFS() float64 {
	if s.OutputFrames == 0 {
		return SilenceFloorDB
	}
	mean := s.SumSquares / float64(s.OutputFrames)
	if mean <= 0 {
		return SilenceFloorDB
	}
	return 10 * math.Log10(mean)
}

// PeakDBFS is the largest absolute sample seen, floored for silence.
func (s Stats) PeakDBFS() float64 { return dBFS(s.MaxAbsSample) }

// Ratio returns clipped/accounted as a fraction, or zero for empty input.
func (s Stats) ClipRatio() float64 {
	if s.OutputFrames == 0 {
		return 0
	}
	return float64(s.ClippedSamples) / float64(s.OutputFrames)
}

// Validate rejects impossible accumulator values.
func (s Stats) Validate() error {
	switch {
	case s.InputFrames < 0, s.OutputFrames < 0, s.ClippedSamples < 0, s.SilentSamples < 0,
		s.Resets < 0, s.TailFrames < 0, s.Segments < 0:
		return fmt.Errorf("audio: negative statistics value")
	case s.OutputFrames > 0 && s.SilentSamples > s.OutputFrames*32:
		return fmt.Errorf("audio: silent samples %d exceed sample count", s.SilentSamples)
	case math.IsNaN(s.SumSquares) || math.IsInf(s.SumSquares, 0):
		return fmt.Errorf("audio: non-finite energy accumulator")
	}
	return nil
}

// Observe adds one output frame's samples to the statistics. Clipping and
// silence are counted per sample; frames and energy per frame.
func (s *Stats) Observe(frame []float64) {
	s.OutputFrames++
	allSilent := true
	for _, v := range frame {
		av := math.Abs(v)
		if av > s.MaxAbsSample {
			s.MaxAbsSample = av
		}
		if av >= 1.0 {
			s.ClippedSamples++
		}
		if av != 0 {
			allSilent = false
		}
		s.SumSquares += v * v
	}
	if allSilent {
		s.SilentSamples += len(frame)
	}
}
