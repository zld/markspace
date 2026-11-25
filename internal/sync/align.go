// Package sync aligns audio, video and subtitle cues onto one master
// integer timeline, measures audio/video drift and fails the stage when the
// configured threshold is exceeded. All times are master clock ticks (see
// internal/clock), so comparisons use no floating point.
package sync

import (
	"fmt"
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/model"
)

// Cue is one timestamped media unit on a track-local time base before
// alignment (frame, audio fragment or subtitle cue).
type Cue struct {
	Track    string `json:"track"`
	Index    int    `json:"index"`
	Rate     int64  `json:"rate"` // source ticks per second
	Start    int64  `json:"start"`
	Duration int64  `json:"duration"`
}

// AlignedCue is a cue projected onto the master timeline.
type AlignedCue struct {
	Track       string `json:"track"`
	Index       int    `json:"index"`
	StartTick   int64  `json:"start_tick"`
	EndTick     int64  `json:"end_tick"`
	SourceStart int64  `json:"source_start"`
}

// AlignmentConfig controls the drift gate.
type AlignmentConfig struct {
	// MaxDriftMillis is the largest permitted |audio-video| offset; the
	// stage fails when any measured drift exceeds it.
	MaxDriftMillis int64
}

// DefaultAlignmentConfig allows up to 40 ms A/V drift.
func DefaultAlignmentConfig() AlignmentConfig {
	return AlignmentConfig{MaxDriftMillis: 40}
}

// Drift is one audio/video comparison at a shared time position.
type Drift struct {
	VideoTick  int64 `json:"video_tick"`
	AudioTick  int64 `json:"audio_tick"`
	DeltaTick  int64 `json:"delta_tick"` // audio - video, may be negative
	AbsTick    int64 `json:"abs_tick"`
}

// Result is the alignment stage output.
type Result struct {
	Cues         []AlignedCue `json:"cues"`
	Drifts       []Drift      `json:"drifts"`
	MaxDriftTick int64        `json:"max_drift_tick"`
	MaxDriftMs   int64        `json:"max_drift_ms"`
	Tracks       []string     `json:"tracks"`
}

// Align projects every cue onto the master timeline, orders the result, and
// measures audio/video drift. An illegal source rate is recorded and fails
// the stage; drift beyond the configured threshold likewise fails, with an
// ALIGNMENT_EXCEEDED record locating the offending interval.
func Align(cues []Cue, cfg AlignmentConfig, rec *model.Recorder) (*Result, error) {
	res := &Result{}
	seenTrack := map[string]bool{}
	for _, c := range cues {
		start, err := clock.Scale(c.Start, c.Rate)
		if err != nil {
			rec.Add(model.ErrorRecord{Code: model.ErrInvalidTimestamp, Track: c.Track,
				Message: fmt.Sprintf("cue %d: %v", c.Index, err)})
			continue
		}
		dur, err := clock.Scale(c.Duration, c.Rate)
		if err != nil {
			rec.Add(model.ErrorRecord{Code: model.ErrInvalidTimestamp, Track: c.Track,
				Message: fmt.Sprintf("cue %d duration: %v", c.Index, err)})
			continue
		}
		res.Cues = append(res.Cues, AlignedCue{
			Track: c.Track, Index: c.Index,
			StartTick: start, EndTick: start + dur, SourceStart: c.Start,
		})
		if !seenTrack[c.Track] {
			seenTrack[c.Track] = true
			res.Tracks = append(res.Tracks, c.Track)
		}
	}
	sort.Strings(res.Tracks)
	sort.SliceStable(res.Cues, func(i, j int) bool {
		if res.Cues[i].StartTick != res.Cues[j].StartTick {
			return res.Cues[i].StartTick < res.Cues[j].StartTick
		}
		if res.Cues[i].Track != res.Cues[j].Track {
			return res.Cues[i].Track < res.Cues[j].Track
		}
		return res.Cues[i].Index < res.Cues[j].Index
	})

	measureDrift(res, rec)
	res.MaxDriftMs = clock.Millis(res.MaxDriftTick)

	limit := clock.Ticks(cfg.MaxDriftMillis)
	if res.MaxDriftTick > limit {
		rec.Add(model.ErrorRecord{Code: model.ErrAlignmentExceeded, Track: "audio/video",
			StartTick: 0, EndTick: res.MaxDriftTick,
			Message: fmt.Sprintf("max A/V drift %d ms exceeds limit %d ms",
				res.MaxDriftMs, cfg.MaxDriftMillis)})
	}
	if rec.HasError() {
		return res, fmt.Errorf("sync: alignment failed: %s", rec.JoinMessages())
	}
	return res, nil
}

// measureDrift pairs each audio cue with the nearest video cue by start
// time and records the absolute offset. Subtitle cues are aligned onto the
// same timeline but do not participate in the A/V drift metric.
func measureDrift(res *Result, rec *model.Recorder) {
	var video, audio []AlignedCue
	for _, c := range res.Cues {
		switch c.Track {
		case "video":
			video = append(video, c)
		case "audio":
			audio = append(audio, c)
		}
	}
	if len(video) == 0 || len(audio) == 0 {
		return // drift undefined when one side is absent; not an error.
	}
	for _, a := range audio {
		v := nearest(video, a.StartTick)
		delta := a.StartTick - v.StartTick
		abs := delta
		if abs < 0 {
			abs = -abs
		}
		res.Drifts = append(res.Drifts, Drift{
			VideoTick: v.StartTick, AudioTick: a.StartTick,
			DeltaTick: delta, AbsTick: abs,
		})
		if abs > res.MaxDriftTick {
			res.MaxDriftTick = abs
		}
	}
}

// nearest returns the video cue whose start is closest to t.
func nearest(video []AlignedCue, t int64) AlignedCue {
	best := video[0]
	bestDist := abs64(best.StartTick - t)
	for _, v := range video[1:] {
		if d := abs64(v.StartTick - t); d < bestDist {
			best, bestDist = v, d
		}
	}
	return best
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
