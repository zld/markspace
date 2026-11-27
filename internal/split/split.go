// Package split cuts a track timeline into segments at sample boundaries.
package split

import (
	"sort"

	"mediaregress/internal/media"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

// DefaultTarget is the nominal segment length. Actual cuts snap to the next
// sample start, so segments are never longer than target plus one sample.
const DefaultTarget = media.Second

// Segment is one output unit. Its window is half-open, which is what makes a
// sample on a cut boundary belong to exactly one segment.
type Segment struct {
	ID                string       `json:"id"`
	Track             string       `json:"track"`
	Kind              track.Kind   `json:"kind"`
	Index             int          `json:"index"`
	Window            media.Window `json:"window"`
	StartSample       int          `json:"start_sample"`
	EndSample         int          `json:"end_sample"` // exclusive
	SampleCount       int          `json:"sample_count"`
	SourceOffset      int64        `json:"source_offset"`
	Size              int64        `json:"size_bytes"`
	StartKeyframe     bool         `json:"start_keyframe"`
	ParseVersion      string       `json:"parse_version"`
	Stage             media.Stage  `json:"stage"`
	TrackParseVersion string       `json:"track_parse_version"`
}

// End returns the exclusive segment end.
func (s Segment) End() media.Ticks { return s.Window.End }

// Tracks splits every timeline track. The returned slice is ordered by track
// ID, then segment index, so manifests are byte-stable.
func Tracks(tl *timeline.Timeline, target media.Ticks) ([]Segment, error) {
	if tl == nil {
		return nil, media.NewError(media.ClassDependency, media.StageSplit, "", -1, "no timeline")
	}
	if target <= 0 {
		return nil, media.Errorf(media.ClassInvalidTimebase, media.StageSplit, "", -1,
			"segment target %d must be positive", target)
	}
	var out []Segment
	tracks := append([]*timeline.TrackTimeline(nil), tl.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].TrackID < tracks[j].TrackID })
	for _, tt := range tracks {
		segs, err := Track(tt, target)
		if err != nil {
			return nil, err
		}
		out = append(out, segs...)
	}
	return out, nil
}

// Track cuts one track. A cut candidate at segStart + k*target advances to the
// first sample start at or after the candidate; candidates that do not advance
// are skipped. Every sample lands in exactly one half-open segment window.
func Track(tt *timeline.TrackTimeline, target media.Ticks) ([]Segment, error) {
	if target <= 0 {
		return nil, media.Errorf(media.ClassInvalidTimebase, media.StageSplit, tt.TrackID, -1,
			"segment target %d must be positive", target)
	}
	if len(tt.Samples) == 0 {
		return nil, media.Errorf(media.ClassCorruptInput, media.StageSplit, tt.TrackID, -1,
			"track has no samples to split")
	}
	// Verify the sample table is ordered before cutting; otherwise ownership
	// is undefined and the first bad sample must be reported.
	for i := 1; i < len(tt.Samples); i++ {
		if tt.Samples[i].Start < tt.Samples[i-1].End() {
			return nil, media.Errorf(media.ClassTimeRegression, media.StageSplit, tt.TrackID,
				tt.Samples[i].Offset, "sample %d overlaps its predecessor", tt.Samples[i].Index)
		}
	}

	first := tt.Samples[0].Start
	lastEnd := tt.Samples[len(tt.Samples)-1].End()
	type cut struct {
		at     media.Ticks
		sample int
	}
	cuts := []cut{{at: first, sample: 0}}
	for candidate := first + target; candidate < lastEnd; candidate += target {
		// First sample that starts at or after the candidate.
		i := sort.Search(len(tt.Samples), func(i int) bool { return tt.Samples[i].Start >= candidate })
		if i >= len(tt.Samples) {
			break
		}
		if tt.Samples[i].Start <= cuts[len(cuts)-1].at {
			continue
		}
		cuts = append(cuts, cut{at: tt.Samples[i].Start, sample: i})
	}

	segs := make([]Segment, 0, len(cuts))
	for k, c := range cuts {
		start := c.at
		var end media.Ticks
		endSample := len(tt.Samples)
		if k+1 < len(cuts) {
			end = cuts[k+1].at
			endSample = cuts[k+1].sample
		} else {
			end = lastEnd
		}
		firstSample := tt.Samples[c.sample]
		var size int64
		var off int64 = -1
		for i := c.sample; i < endSample; i++ {
			size += int64(tt.Samples[i].Size)
			if off < 0 && tt.Samples[i].Offset >= 0 {
				off = tt.Samples[i].Offset
			}
		}
		segs = append(segs, Segment{
			ID:                segID(tt.TrackID, k),
			Track:             tt.TrackID,
			Kind:              tt.Kind,
			Index:             k,
			Window:            media.Window{Start: start, End: end},
			StartSample:       c.sample,
			EndSample:         endSample,
			SampleCount:       endSample - c.sample,
			SourceOffset:      off,
			Size:              size,
			StartKeyframe:     firstSample.Keyframe,
			ParseVersion:      tt.ParseVersion,
			TrackParseVersion: tt.ParseVersion,
			Stage:             media.StageSplit,
		})
	}
	return segs, nil
}

func segID(track string, index int) string {
	digits := []byte{}
	if index == 0 {
		digits = []byte{'0'}
	}
	for v := index; v > 0; v /= 10 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
	}
	for len(digits) < 4 {
		digits = append([]byte{'0'}, digits...)
	}
	return track + "/" + string(digits)
}

// Owner returns the segment containing the sample at index i.
func Owner(segs []Segment, trackID string, sample int) (Segment, bool) {
	for _, s := range segs {
		if s.Track == trackID && sample >= s.StartSample && sample < s.EndSample {
			return s, true
		}
	}
	return Segment{}, false
}

// Validate checks the invariants a manifest relies on: ordered, contiguous per
// track, non-overlapping, and covering every sample exactly once.
func Validate(segs []Segment) error {
	perTrack := map[string][]Segment{}
	for _, s := range segs {
		if s.Window.End <= s.Window.Start {
			return media.Errorf(media.ClassOutOfRange, media.StageSplit, s.Track, s.SourceOffset,
				"segment %s has an empty window", s.ID)
		}
		if err := s.Window.Validate(); err != nil {
			return err
		}
		perTrack[s.Track] = append(perTrack[s.Track], s)
	}
	for id, list := range perTrack {
		sort.Slice(list, func(i, j int) bool { return list[i].Window.Start < list[j].Window.Start })
		for i := 1; i < len(list); i++ {
			if list[i].StartSample < list[i-1].EndSample {
				return media.Errorf(media.ClassOutOfRange, media.StageSplit, id, list[i].SourceOffset,
					"segments %s and %s overlap in samples", list[i-1].ID, list[i].ID)
			}
			if list[i].StartSample != list[i-1].EndSample {
				return media.Errorf(media.ClassOutOfRange, media.StageSplit, id, list[i].SourceOffset,
					"segments %s and %s leave a sample gap", list[i-1].ID, list[i].ID)
			}
			if list[i].Window.Start < list[i-1].Window.End {
				return media.Errorf(media.ClassTimeRegression, media.StageSplit, id, list[i].SourceOffset,
					"segment %s starts before %s ends", list[i].ID, list[i-1].ID)
			}
		}
	}
	return nil
}
