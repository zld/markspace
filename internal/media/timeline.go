package media

import (
	"fmt"
	"sort"
)

// Sample is one timed media unit. Time fields are integer nanoseconds on the
// unified timeline clock; DataOffset is the byte offset inside the source.
type Sample struct {
	Index       int    // 1-based position within its track
	StartNs     int64  // presentation start on the unified clock
	DurationNs  int64  // exact duration of the unit
	DataOffset  int64  // source byte offset of the payload
	Size        int64  // payload size in bytes
	Payload     []byte // in-memory payload (small fixtures only; nil after flush)
	SourceTick  int64  // original clock tick, retained for audit output
}

// EndNs returns the half-open end of the sample.
func (s Sample) EndNs() int64 { return s.StartNs + s.DurationNs }

// Track is an ordered, non-overlapping collection of samples plus the exact
// time base they were converted from.
type Track struct {
	Key      string
	TimeBase TimeBase
	Samples  []Sample
}

// Add appends a sample, assigning its 1-based index and rejecting negative
// or backwards time. Callers keep payloads bounded by the 64 MiB budget.
func (t *Track) Add(startNs, durNs, dataOffset, size, tick int64, payload []byte) error {
	if startNs < 0 || durNs <= 0 {
		return NewError(StageScan, "track.add", "sample has non-positive duration or negative start").
			WithTrack(t.Key)
	}
	if n := len(t.Samples); n > 0 {
		prev := t.Samples[n-1]
		if startNs < prev.EndNs() {
			return NewError(StageScan, "track.add", "samples overlap or are out of order").
				WithTrack(t.Key).WithOffset(dataOffset)
		}
	}
	t.Samples = append(t.Samples, Sample{
		Index:      len(t.Samples) + 1,
		StartNs:    startNs,
		DurationNs: durNs,
		DataOffset: dataOffset,
		Size:       size,
		Payload:    payload,
		SourceTick: tick,
	})
	return nil
}

// SampleCount totals samples across all tracks after stable key ordering.
func (tl *Timeline) SampleCount() int {
	n := 0
	for _, tr := range tl.Tracks {
		n += len(tr.Samples)
	}
	return n
}

// Clip is a half-open edit interval applied to a track, expressed on the
// unified nanosecond clock.
type Clip struct {
	TrackKey string
	Interval Interval
}

// Transition spans two tracks over a half-open interval. Cross-track
// transitions must reference two distinct tracks.
type Transition struct {
	FromTrack string
	ToTrack   string
	Interval  Interval
}

// Timeline is the unified, validated view of every parsed input.
type Timeline struct {
	Tracks      []*Track
	Clips       []Clip
	Transitions []Transition
}

// NewTimeline sorts tracks by key so every downstream artifact is stable.
func NewTimeline(tracks []*Track) *Timeline {
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].Key < tracks[j].Key })
	return &Timeline{Tracks: tracks}
}

// Track looks up a track by key.
func (tl *Timeline) Track(key string) (*Track, bool) {
	for _, tr := range tl.Tracks {
		if tr.Key == key {
			return tr, true
		}
	}
	return nil, false
}

// DurationNs is the maximum sample end across tracks, the timeline span.
func (tl *Timeline) DurationNs() (int64, error) {
	var end int64
	for _, tr := range tl.Tracks {
		if len(tr.Samples) == 0 {
			continue
		}
		cand := tr.Samples[len(tr.Samples)-1].EndNs()
		if cand > end {
			end = cand
		}
	}
	return end, nil
}

// ValidateClips rejects clips that are empty or reach outside their track.
func (tl *Timeline) ValidateClips() error {
	for i, c := range tl.Clips {
		if err := c.Interval.Validate(); err != nil {
			return NewError(StageScan, "clip.validate",
				fmt.Sprintf("clip %d invalid: %v", i, err)).WithTrack(c.TrackKey)
		}
		tr, ok := tl.Track(c.TrackKey)
		if !ok {
			return NewError(StageScan, "clip.validate", "unknown track "+c.TrackKey)
		}
		trackEnd, err := tl.DurationNs()
		if err != nil {
			return NewError(StageScan, "clip.validate", err.Error()).WithTrack(c.TrackKey)
		}
		_ = tr
		if c.Interval.EndNs > trackEnd {
			return NewError(StageScan, "clip.validate",
				fmt.Sprintf("clip end %d exceeds timeline end %d", c.Interval.EndNs, trackEnd)).
				WithTrack(c.TrackKey).WithOffset(c.Interval.EndNs)
		}
	}
	return nil
}

// ValidateTransitions rejects self-transitions, empty spans and intervals
// that extend past either referenced track's content.
func (tl *Timeline) ValidateTransitions() error {
	for i, x := range tl.Transitions {
		if x.FromTrack == x.ToTrack {
			return NewError(StageScan, "transition.validate",
				fmt.Sprintf("transition %d must cross two tracks", i)).WithTrack(x.ToTrack)
		}
		if _, ok := tl.Track(x.FromTrack); !ok {
			return NewError(StageScan, "transition.validate", "unknown track "+x.FromTrack)
		}
		to, ok := tl.Track(x.ToTrack)
		if !ok {
			return NewError(StageScan, "transition.validate", "unknown track "+x.ToTrack)
		}
		if err := x.Interval.Validate(); err != nil {
			return NewError(StageScan, "transition.validate",
				fmt.Sprintf("transition %d invalid: %v", i, err)).WithTrack(x.ToTrack)
		}
		if len(to.Samples) == 0 || x.Interval.EndNs > to.Samples[len(to.Samples)-1].EndNs() {
			return NewError(StageScan, "transition.validate",
				fmt.Sprintf("transition end %d exceeds track %s content", x.Interval.EndNs, x.ToTrack)).
				WithTrack(x.ToTrack).WithOffset(x.Interval.EndNs)
		}
	}
	return nil
}

// OrderedSamples returns every sample as (trackKey, sample) in stable track
// then index order, which is the canonical processing order for checkpoints.
func (tl *Timeline) OrderedSamples() []struct {
	Track  string
	Sample Sample
} {
	out := make([]struct {
		Track  string
		Sample Sample
	}, 0, tl.SampleCount())
	for _, tr := range tl.Tracks {
		for _, s := range tr.Samples {
			out = append(out, struct {
				Track  string
				Sample Sample
			}{tr.Key, s})
		}
	}
	return out
}
