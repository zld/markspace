// Package timeline merges parsed containers onto one shared integer-tick
// clock. Track-local timescales are converted with floor division; coincident
// boundaries keep a stable ordering by track number then sample index.
package timeline

import (
	"fmt"

	"mediapack/internal/clock"
	"mediapack/internal/ingest"
	"mediapack/internal/model"
)

// TrackSegment is one track projected onto the master tick clock.
type TrackSegment struct {
	GlobalIndex int                  `json:"global_index"`
	Container   int                  `json:"container"`
	LocalIndex  int                  `json:"local_index"`
	Kind        ingest.TrackKind     `json:"kind"`
	StartTick   int64                `json:"start_tick"`
	EndTick     int64                `json:"end_tick"`
	SampleCount int                  `json:"sample_count"`
	Timescale   uint32               `json:"timescale"`
	Alignment   clock.AlignmentError `json:"alignment"`
	Track       *ingest.Track        `json:"-"`
}

// Timeline is the composed, shared-clock view of every input track.
type Timeline struct {
	Segments      []TrackSegment        `json:"segments"`
	Points        []clock.TimelinePoint `json:"points"`
	DurationTicks int64                 `json:"duration_ticks"`
	MaxAlignment  clock.AlignmentError  `json:"max_alignment"`
}

// ComposeUnique projects the deduplicated logical track set (MP4 and MXF
// describe the same media once each) onto the master tick clock.
func ComposeUnique(containers ...*ingest.Container) (*Timeline, error) {
	return composeTracks(ingest.DeduplicateTracks(containers...))
}

// Compose projects every track of every container onto the master clock.
// Containers are concatenated in argument order; global track indices are
// assigned in that same order and never reused.
func Compose(containers ...*ingest.Container) (*Timeline, error) {
	var tracks []*ingest.Track
	for _, c := range containers {
		if c == nil {
			continue
		}
		tracks = append(tracks, c.Tracks...)
	}
	return composeTracks(tracks)
}

// composeTracks projects an explicit track list onto the master tick clock.
func composeTracks(tracks []*ingest.Track) (*Timeline, error) {
	tl := &Timeline{}
	global := 0
	for _, t := range tracks {
		conv, err := clock.NewConverter(t.Timescale)
		if err != nil {
			src := t.Source("timescale")
			return nil, model.WrapField(model.KindInvalidInput, src.File,
				src.Offset, src.Path,
				fmt.Sprintf("track %d has unusable timescale", global), err)
		}
		seg := TrackSegment{
			GlobalIndex: global,
			Kind:        t.Kind,
			Timescale:   t.Timescale,
			SampleCount: len(t.Samples),
			Track:       t,
		}
		// Sample boundaries: cumulative source time converted per boundary.
		var srcTime int64
		for si, s := range t.Samples {
			tick := conv.ToTicks(srcTime)
			tl.Points = append(tl.Points, clock.TimelinePoint{
				Tick:        tick,
				TrackIndex:  global,
				SampleIndex: si,
			})
			srcTime += int64(s.Delta)
		}
		seg.StartTick = 0
		seg.EndTick = conv.ToTicks(srcTime)
		seg.Alignment = conv.AlignmentError()
		tl.Segments = append(tl.Segments, seg)
		if seg.EndTick > tl.DurationTicks {
			tl.DurationTicks = seg.EndTick
		}
		global++
	}
	clock.SortPoints(tl.Points)
	for _, seg := range tl.Segments {
		tl.MaxAlignment = clock.MergeMax(tl.MaxAlignment, seg.Alignment)
	}
	return tl, nil
}

// VideoSegments returns the composed video tracks in global index order.
func (t *Timeline) VideoSegments() []TrackSegment {
	return t.segmentsOf(ingest.TrackVideo)
}

// AudioSegments returns the composed audio tracks in global index order.
func (t *Timeline) AudioSegments() []TrackSegment {
	return t.segmentsOf(ingest.TrackAudio)
}

func (t *Timeline) segmentsOf(k ingest.TrackKind) []TrackSegment {
	var out []TrackSegment
	for _, s := range t.Segments {
		if s.Kind == k {
			out = append(out, s)
		}
	}
	return out
}
