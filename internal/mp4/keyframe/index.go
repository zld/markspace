// Package keyframe computes keyframe intervals on scanned MP4 tracks and
// locates over-long intervals as timeline ranges.
package keyframe

import (
	"fmt"

	mp4scan "continuity/internal/mp4"
	"continuity/internal/timeline"
)

// Interval spans from one keyframe to the next (exclusive) on the track clock.
type Interval struct {
	StartSample int // index of the keyframe starting this interval
	EndSample   int // index of the next keyframe, or len(samples)
	Start       timeline.Ticks
	End         timeline.Ticks
}

// Length is the interval duration in track ticks.
func (i Interval) Length() timeline.Ticks { return i.End - i.Start }

// Intervals returns the keyframe-to-keyframe intervals of a track. A track
// with no keyframe flags yields a single interval covering all samples.
func Intervals(t *mp4scan.Track) []Interval {
	var keys []int
	for i, s := range t.Samples {
		if s.Key {
			keys = append(keys, i)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	var out []Interval
	for k, ki := range keys {
		end := len(t.Samples)
		if k+1 < len(keys) {
			end = keys[k+1]
		}
		out = append(out, Interval{
			StartSample: ki,
			EndSample:   end,
			Start:       t.Samples[ki].Start,
			End:         t.Samples[end-1].Start + t.Samples[end-1].Duration,
		})
	}
	return out
}

// FlagLong converts intervals longer than maxLen (track ticks) into damaged
// timeline ranges carrying sample offsets and start/end times.
func FlagLong(t *mp4scan.Track, maxLen timeline.Ticks) []timeline.Range {
	var out []timeline.Range
	name := fmt.Sprintf("%s/mp4#%d", t.Kind, t.ID)
	for _, iv := range Intervals(t) {
		if iv.Length() > maxLen {
			out = append(out, timeline.Range{
				Track:       name,
				Reason:      "keyframe interval too long",
				StartOffset: int64(iv.StartSample),
				EndOffset:   int64(iv.EndSample - 1),
				Start:       iv.Start,
				End:         iv.End,
			})
		}
	}
	return out
}
