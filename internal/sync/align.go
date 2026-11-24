// Package sync aligns audio and video tracks on the unified nanosecond
// timeline and quantifies A/V synchronization error. Unrecoverable regions
// are never dropped from the statistics: each contributes an explicit
// measurement so the summary reflects the whole stream, not just the clean
// middle.
package sync

import (
	"math"
	"sort"

	"mediacheck/internal/damage"
	"mediacheck/internal/media"
)

// Measurement is one A/V sync sample: the offset of the nearest audio point
// from a video point on the unified timeline.
type Measurement struct {
	VideoNanos int64 `json:"video_nanos"`
	AudioNanos int64 `json:"audio_nanos"`
	ErrorNanos int64 `json:"error_nanos"`
	// InDamage means the point falls inside an unrecoverable range; the
	// measurement is still included, only flagged.
	InDamage bool `json:"in_damage"`
}

// Report summarizes A/V alignment across the whole timeline.
type Report struct {
	VideoTrackID uint32        `json:"video_track_id"`
	AudioTrackID uint32        `json:"audio_track_id"`
	Measurements []Measurement `json:"measurements"`
	// ErrorMs: signed mean, absolute mean, max absolute, p95 absolute.
	MeanErrorMs float64 `json:"mean_error_ms"`
	AbsMeanMs   float64 `json:"abs_mean_error_ms"`
	MaxAbsMs    int64   `json:"max_abs_error_ms"`
	P95AbsMs    int64   `json:"p95_abs_ms"`
	// DamageMeasurements counts samples explicitly kept from bad regions.
	DamageMeasurements int `json:"damage_measurements"`
}

// Align pairs every video point with its temporally nearest audio point and
// records the signed offset (audio minus video). Damage ranges are stamped
// onto the measurements they contain rather than removed.
func Align(video, audio *media.TrackLine, damages []damage.Range) *Report {
	rep := &Report{VideoTrackID: video.TrackID, AudioTrackID: audio.TrackID}
	audioStarts := make([]int64, len(audio.Points))
	for i, p := range audio.Points {
		audioStarts[i] = p.StartNanos
	}
	for _, vp := range video.Points {
		nearest := nearestStart(audioStarts, vp.StartNanos)
		err := nearest - vp.StartNanos
		rep.Measurements = append(rep.Measurements, Measurement{
			VideoNanos: vp.StartNanos,
			AudioNanos: nearest,
			ErrorNanos: err,
			InDamage:   insideAny(vp.StartNanos, damages),
		})
	}
	rep.summarize()
	return rep
}

// nearestStart returns the value in the sorted starts closest to target.
func nearestStart(starts []int64, target int64) int64 {
	i := sort.Search(len(starts), func(i int) bool { return starts[i] >= target })
	switch {
	case i == 0:
		return starts[0]
	case i == len(starts):
		return starts[len(starts)-1]
	}
	lo, hi := starts[i-1], starts[i]
	if target-lo <= hi-target {
		return lo
	}
	return hi
}

func insideAny(t int64, ranges []damage.Range) bool {
	for _, r := range ranges {
		if t >= r.StartNanos && t < r.EndNanos {
			return true
		}
	}
	return false
}

func (r *Report) summarize() {
	if len(r.Measurements) == 0 {
		return
	}
	abs := make([]int64, 0, len(r.Measurements))
	var sum float64
	var absSum int64
	for _, m := range r.Measurements {
		if m.InDamage {
			r.DamageMeasurements++
		}
		a := m.ErrorNanos
		if a < 0 {
			a = -a
		}
		abs = append(abs, a)
		absSum += a
		sum += float64(m.ErrorNanos)
	}
	n := float64(len(r.Measurements))
	r.MeanErrorMs = (sum / n) / 1e6
	r.AbsMeanMs = (float64(absSum) / n) / 1e6
	sort.Slice(abs, func(i, j int) bool { return abs[i] < abs[j] })
	r.MaxAbsMs = abs[len(abs)-1] / 1e6
	rank := (95*len(abs) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	r.P95AbsMs = abs[rank-1] / 1e6
}

// WithinTolerance reports whether the absolute mean error is within the
// given millisecond budget.
func (r *Report) WithinTolerance(budgetMs float64) bool {
	return !math.IsNaN(r.AbsMeanMs) && r.AbsMeanMs <= budgetMs
}
