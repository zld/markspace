// Package index builds sparse random-access indexes and executes event-time queries.
package index

import (
	"sort"
	"strconv"

	"mediacore/internal/clock"
	"mediacore/internal/mp4"
)

// Keyframe is a verified random-access point.
type Keyframe struct {
	SampleNumber int             `json:"sample_number"`
	PTS          clock.MediaTime `json:"pts"`
	EventStart   clock.EventTime `json:"event_start_ns"`
	FileOffset   int64           `json:"raw_offset"`
	Size         uint32          `json:"size"`
}

// TimedSample adds common event times to an MP4 sample.
type TimedSample struct {
	mp4.Sample
	EventStart clock.EventTime `json:"event_start_ns"`
	EventEnd   clock.EventTime `json:"event_end_ns"`
}

// Index is one track's queryable keyframe and sample index.
type Index struct {
	Key       string
	Path      string
	Track     int
	TimeScale clock.TicksPerSecond
	TimeMap   *clock.TimeMap
	Keyframes []Keyframe
	Samples   []TimedSample
}

// Build creates an index from a parsed MP4 track. Only sync samples with
// positive, in-file byte ranges are admitted as random-access keyframes.
func Build(key string, fileSize int64, t *mp4.Track) (*Index, error) {
	if t == nil {
		return nil, clock.New(clock.CategoryMissingTrack, "", 0, 0, "nil track")
	}
	tm, err := buildTimeMap(t)
	if err != nil {
		return nil, err
	}
	idx := &Index{Key: key, Path: t.Path, Track: t.Number, TimeScale: t.TimeScale, TimeMap: tm}
	for _, s := range t.Samples {
		start, err := tm.MediaToEvent(clock.MediaTime(s.PTS))
		if err != nil {
			// Samples outside a mapped edit are not part of the event timeline.
			continue
		}
		end := start + t.TimeScale.NS(int64(s.Delta))
		idx.Samples = append(idx.Samples, TimedSample{Sample: s, EventStart: start, EventEnd: end})
		if s.Sync && decodableLocation(s, fileSize) {
			idx.Keyframes = append(idx.Keyframes, Keyframe{
				SampleNumber: s.Number, PTS: clock.MediaTime(s.PTS), EventStart: start,
				FileOffset: s.Offset, Size: s.Size,
			})
		}
	}
	sort.Slice(idx.Keyframes, func(i, j int) bool { return idx.Keyframes[i].EventStart < idx.Keyframes[j].EventStart })
	return idx, nil
}

func buildTimeMap(t *mp4.Track) (*clock.TimeMap, error) {
	if t.HasEdit {
		movie := t.MovieScale
		if movie == 0 {
			movie = t.TimeScale
		}
		return clock.BuildEditMap(t.Path, t.Number, movie, t.TimeScale, t.Edits)
	}
	first := clock.MediaTime(0)
	duration := int64(0)
	if len(t.Samples) > 0 {
		first = clock.MediaTime(t.Samples[0].PTS)
		last := t.Samples[len(t.Samples)-1]
		duration = last.PTS + int64(last.Delta) - int64(first)
	}
	return clock.NewIdentityMap(t.Path, t.Number, t.TimeScale, first, duration)
}

func decodableLocation(s mp4.Sample, fileSize int64) bool {
	if s.Size == 0 || s.Offset <= 0 {
		return false
	}
	end := s.Offset + int64(s.Size)
	if fileSize > 0 && end > fileSize {
		return false
	}
	return true
}

// VerifyExpectedKeyframes checks a deterministic RAP cadence against the index.
// It is used by corruption fixtures where an stss/index item is omitted.
func (idx *Index) VerifyExpectedKeyframes(every, totalSamples int) error {
	if every <= 0 {
		return clock.New(clock.CategoryInvalidArgument, idx.Path, idx.Track, 0, "keyframe cadence must be positive")
	}
	got := map[int]bool{}
	for _, k := range idx.Keyframes {
		got[k.SampleNumber] = true
	}
	for zero := 0; zero < totalSamples; zero += every {
		n := zero + 1
		if !got[n] {
			off := int64(0)
			if n-1 < len(idx.Samples) {
				off = idx.Samples[n-1].Offset
			}
			return clock.New(clock.CategoryMissingKeyframe, idx.Path, idx.Track, off,
				"keyframe index is missing sample "+strconv.Itoa(n))
		}
	}
	return nil
}
