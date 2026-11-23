// Package audio detects silence windows on the unified timeline. It works
// from the PCM content attached to audio samples and reports deterministic,
// non-overlapping silence intervals in nanoseconds.
package audio

import (
	"math"

	"mediapipe/internal/model"
)

// Config controls silence detection.
type Config struct {
	// Threshold is the RMS amplitude below which a window counts as silent.
	Threshold float64
	// WindowNanos is the analysis window length on the session nano axis.
	WindowNanos int64
	// MergeGapNanos merges two silent windows separated by at most this much.
	MergeGapNanos int64
	// MinSilenceNanos drops post-merge windows shorter than this length.
	MinSilenceNanos int64
}

// DefaultConfig returns the thresholds used by the acceptance demo.
func DefaultConfig() Config {
	return Config{
		Threshold:       200.0,
		WindowNanos:     20_000_000, // 20 ms
		MergeGapNanos:   80_000_000, // 80 ms
		MinSilenceNanos: 40_000_000, // 40 ms
	}
}

// Interval is one silence window in session nanoseconds.
type Interval struct {
	StartNanos int64 `json:"start_ns"`
	EndNanos   int64 `json:"end_ns"`
}

// Length returns the interval duration, always non-negative.
func (i Interval) Length() int64 {
	if i.EndNanos < i.StartNanos {
		return 0
	}
	return i.EndNanos - i.StartNanos
}

// Result holds silence analysis output.
type Result struct {
	Intervals   []Interval `json:"intervals"`
	TotalNanos  int64      `json:"total_silence_ns"`
	RMS         float64    `json:"rms"`
	Windows     int        `json:"windows"`
	SilentWindows int      `json:"silent_windows"`
}

// Analyze scans an audio track's PCM and returns silence intervals and the
// overall RMS. PCM is read from Sample.Pcm; missing PCM is treated as silence
// only when every sample of a window lacks content.
func Analyze(tr *model.Track, cfg Config) Result {
	if tr == nil || len(tr.Samples) == 0 || cfg.WindowNanos <= 0 {
		return Result{}
	}
	var sumSq float64
	var totalSamples int
	for _, s := range tr.Samples {
		for _, v := range s.Pcm {
			x := float64(v)
			sumSq += x * x
			totalSamples++
		}
	}
	res := Result{}
	if totalSamples > 0 {
		res.RMS = math.Sqrt(sumSq / float64(totalSamples))
	}

	// Flatten PCM onto the nano axis using each sample's session PTS.
	type point struct {
		t   int64
		val int16
	}
	var points []point
	for _, s := range tr.Samples {
		base := s.PTS + tr.OffsetNanos
		step := int64(0)
		if len(s.Pcm) > 0 && s.DurationNanos > 0 {
			step = s.DurationNanos / int64(len(s.Pcm))
		}
		for i, v := range s.Pcm {
			points = append(points, point{base + step*int64(i), v})
		}
	}
	if len(points) == 0 {
		return res
	}
	start := points[0].t
	end := points[len(points)-1].t

	var raw []Interval
	for wStart := start; wStart < end; wStart += cfg.WindowNanos {
		wEnd := wStart + cfg.WindowNanos
		if wEnd > end {
			wEnd = end
		}
		res.Windows++
		var wSumSq float64
		var wN int
		for _, p := range points {
			if p.t < wStart {
				continue
			}
			if p.t >= wEnd {
				break
			}
			x := float64(p.val)
			wSumSq += x * x
			wN++
		}
		var rms float64
		if wN > 0 {
			rms = math.Sqrt(wSumSq / float64(wN))
		}
		if rms <= cfg.Threshold {
			res.SilentWindows++
			raw = append(raw, Interval{StartNanos: wStart, EndNanos: wEnd})
		}
	}
	res.Intervals = mergeIntervals(raw, cfg.MergeGapNanos, cfg.MinSilenceNanos)
	for _, iv := range res.Intervals {
		res.TotalNanos += iv.Length()
	}
	return res
}

// mergeIntervals joins windows separated by at most mergeGap and drops results
// shorter than minLen. Output stays sorted and non-overlapping.
func mergeIntervals(in []Interval, mergeGap, minLen int64) []Interval {
	if len(in) == 0 {
		return nil
	}
	var merged []Interval
	cur := in[0]
	for _, iv := range in[1:] {
		if iv.StartNanos-cur.EndNanos <= mergeGap {
			if iv.EndNanos > cur.EndNanos {
				cur.EndNanos = iv.EndNanos
			}
			continue
		}
		merged = append(merged, cur)
		cur = iv
	}
	merged = append(merged, cur)
	out := merged[:0]
	for _, iv := range merged {
		if iv.Length() >= minLen {
			out = append(out, iv)
		}
	}
	return out
}
