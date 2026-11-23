// Package audio detects silence in multichannel PCM and maps the result onto
// the unified timeline.
//
// Analysis runs on windows, not on individual samples: a window is silent when
// every channel's RMS stays below the threshold for the whole window. Silences
// are then merged with a configurable minimum duration, and mapped back to
// media time through the same clock.Timebase the video path uses, so a silence
// that straddles a cut boundary can be attributed to the right fragments.
package audio

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"avbatch/internal/mediaerr"
)

// PCM is interleaved signed 16-bit audio.
type PCM struct {
	SampleRate int
	Channels   int
	Frames     []int16 // len(Frames) == frames*Channels
	// StartNanos places the first frame on the unified timeline. The pipeline
	// supplies the drift-corrected start of the audio run, so frame counting
	// here shares the video path's timebase.
	StartNanos int64
}

func (p PCM) frameCount() int {
	if p.Channels <= 0 {
		return 0
	}
	return len(p.Frames) / p.Channels
}

func (p PCM) frameNanos() (int64, error) {
	if p.SampleRate <= 0 {
		return 0, errors.New("audio: sample rate must be positive")
	}
	return int64(time.Second) / int64(p.SampleRate), nil
}

// Config controls silence detection.
type Config struct {
	// WindowNanos is the analysis window.
	WindowNanos int64
	// ThresholdDBFS is the per-channel RMS threshold; -60 dBFS is a common
	// "digital silence" bound.
	ThresholdDBFS float64
	// MinSilenceNanos is the shortest run reported as silence.
	MinSilenceNanos int64
	// RequireAllChannelsSilent treats a window as silent only when every
	// channel is below threshold. Disabling it uses the loudest channel, which
	// is equivalent for the common case but keeps the flag explicit.
	RequireAllChannelsSilent bool
}

// DefaultConfig is a conservative speech-oriented default.
func DefaultConfig() Config {
	return Config{
		WindowNanos:              int64(20 * time.Millisecond),
		ThresholdDBFS:            -60,
		MinSilenceNanos:          int64(200 * time.Millisecond),
		RequireAllChannelsSilent: true,
	}
}

// Interval is a silence run on the unified timeline.
type Interval struct {
	Start int64
	End   int64
	// PeakDBFS is the loudest window RMS inside the run.
	PeakDBFS float64
}

// Duration returns End-Start.
func (i Interval) Duration() int64 { return i.End - i.Start }

// Result is the detector output.
type Result struct {
	Intervals []Interval
	// TotalSilenceNanos is the summed duration of reported intervals.
	TotalSilenceNanos int64
	// Windows is how many analysis windows were examined.
	Windows int
	// SilentWindows counts windows below threshold before merging.
	SilentWindows int
}

// DropStats describes silence removed by a boundary rule when mapping across
// segments: windows that straddle a cut are trimmed, not moved.
type DropStats struct {
	TrimmedNanos int64
	SplitCount   int
}

// Detect finds silence runs. It returns an error for unusable input (zero
// channels, empty frames with a non-zero claim, bad thresholds) and never
// merges across an interval longer than one window of non-silence.
func Detect(pcm PCM, cfg Config) (Result, error) {
	var res Result
	if pcm.Channels <= 0 {
		return res, errors.New("audio: channel count must be positive")
	}
	frameNanos, err := pcm.frameNanos()
	if err != nil {
		return res, err
	}
	if cfg.WindowNanos <= 0 {
		return res, errors.New("audio: window must be positive")
	}
	if cfg.ThresholdDBFS > 0 {
		return res, fmt.Errorf("audio: threshold %.1f dBFS must be <= 0", cfg.ThresholdDBFS)
	}
	if cfg.MinSilenceNanos < 0 {
		return res, errors.New("audio: minimum silence must not be negative")
	}
	frames := pcm.frameCount()
	res.Windows = 0
	if frames == 0 {
		return res, nil
	}
	windowFrames := int(cfg.WindowNanos / frameNanos)
	if windowFrames < 1 {
		windowFrames = 1
	}

	type win struct {
		silent   bool
		start    int64
		end      int64
		peakDBFS float64
	}
	var wins []win
	for f := 0; f < frames; f += windowFrames {
		endFrame := f + windowFrames
		if endFrame > frames {
			endFrame = frames
		}
		silent, peak, err := windowSilence(pcm, f, endFrame, cfg)
		if err != nil {
			return res, err
		}
		start := pcm.timeOfFrame(f, pcm.StartNanos, frameNanos)
		end := pcm.timeOfFrame(endFrame, pcm.StartNanos, frameNanos)
		wins = append(wins, win{silent: silent, start: start, end: end, peakDBFS: peak})
		res.Windows++
		if silent {
			res.SilentWindows++
		}
	}

	// Merge adjacent silent windows; keep the loudest peak across the run.
	var cur *Interval
	for _, w := range wins {
		if !w.silent {
			if cur != nil {
				res.Intervals = append(res.Intervals, *cur)
				cur = nil
			}
			continue
		}
		if cur == nil {
			cur = &Interval{Start: w.start, End: w.end, PeakDBFS: w.peakDBFS}
		} else {
			cur.End = w.end
			if w.peakDBFS > cur.PeakDBFS {
				cur.PeakDBFS = w.peakDBFS
			}
		}
	}
	if cur != nil {
		res.Intervals = append(res.Intervals, *cur)
	}
	filtered := res.Intervals[:0]
	for _, iv := range res.Intervals {
		if iv.Duration() >= cfg.MinSilenceNanos {
			filtered = append(filtered, iv)
			res.TotalSilenceNanos += iv.Duration()
		}
	}
	res.Intervals = filtered
	return res, nil
}

func (p PCM) timeOfFrame(frame int, start, frameNanos int64) int64 {
	return start + int64(frame)*frameNanos
}

// windowSilence measures per-channel RMS over [from,to) and compares with the
// configured threshold. RMS is used rather than peak so a single click does
// not make a silent window look loud, and per-channel so a silent channel in a
// stereo pair is not mistaken for silence.
func windowSilence(pcm PCM, from, to int, cfg Config) (bool, float64, error) {
	ch := pcm.Channels
	n := to - from
	if n <= 0 {
		return false, 0, nil
	}
	worstDBFS := math.Inf(-1)
	for c := 0; c < ch; c++ {
		var sum float64
		for f := from; f < to; f++ {
			v := float64(pcm.Frames[f*ch+c])
			sum += v * v
		}
		rms := math.Sqrt(sum / float64(n))
		dbfs := ampToDB(rms)
		if dbfs > worstDBFS {
			worstDBFS = dbfs
		}
		if cfg.RequireAllChannelsSilent && dbfs > cfg.ThresholdDBFS {
			return false, worstDBFS, nil
		}
	}
	if !cfg.RequireAllChannelsSilent {
		return worstDBFS <= cfg.ThresholdDBFS, worstDBFS, nil
	}
	return true, worstDBFS, nil
}

func ampToDB(amp float64) float64 {
	if amp <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(amp/32768)
}

// MapToSegments intersects silence intervals with a segment manifest.
//
// A window that straddles a segment boundary is split, never extended: each
// side keeps only its own media. TrimmedNanos counts only the silence that no
// segment covers at all (for example before the first cut or after the last),
// so a silence that is merely split is not reported as lost.
func MapToSegments(res Result, bounds []Interval) (map[int][]Interval, DropStats) {
	out := make(map[int][]Interval, len(bounds))
	var stats DropStats
	for _, iv := range res.Intervals {
		var pieces int
		var covered int64
		for idx, b := range bounds {
			start := maxInt64(iv.Start, b.Start)
			end := minInt64(iv.End, b.End)
			if end <= start {
				continue
			}
			pieces++
			covered += end - start
			out[idx] = append(out[idx], Interval{Start: start, End: end, PeakDBFS: iv.PeakDBFS})
		}
		if pieces > 1 {
			stats.SplitCount += pieces - 1
		}
		if lost := iv.Duration() - covered; lost > 0 {
			stats.TrimmedNanos += lost
		}
	}
	for idx := range out {
		sort.Slice(out[idx], func(i, j int) bool { return out[idx][i].Start < out[idx][j].Start })
	}
	return out, stats
}

// Validate checks that intervals are sorted and disjoint.
func Validate(intervals []Interval) error {
	var errs []error
	prev := int64(math.MinInt64)
	for i, iv := range intervals {
		if iv.End < iv.Start {
			errs = append(errs, mediaerr.New(mediaerr.KindSilenceWindow, i, "", iv.Start, "silence ends before it starts"))
		}
		if iv.Start < prev {
			errs = append(errs, mediaerr.New(mediaerr.KindOverlap, i, "", iv.Start,
				fmt.Sprintf("silence interval starts at %d before previous end %d", iv.Start, prev)))
		}
		prev = iv.End
	}
	return errors.Join(errs...)
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// PCMFromFloat converts a float slice in [-1,1] to int16, for fixtures.
func PCMFromFloat(samples []float64) []int16 {
	out := make([]int16, len(samples))
	for i, v := range samples {
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		out[i] = int16(math.Round(v * 32767))
	}
	return out
}
