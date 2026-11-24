// Package timeline composes parsed, normalised tracks into one nanosecond
// timeline. Invalid tracks are excluded with a typed error rather than
// contributing frames; valid tracks still compose.
package timeline

import (
	"fmt"
	"sort"

	"archive/internal/clock"
	"archive/internal/normalize"
	"archive/internal/probe"
)

// Clip is one edit interval placed on the shared timeline.
type Clip struct {
	Index         int
	SourceStartNS int64 // offset into the source media
	StartNS       int64 // timeline start
	DurationNS    int64
	FrameCount    int
}

// EndNS is the exclusive timeline end.
func (c Clip) EndNS() int64 { return c.StartNS + c.DurationNS }

// Track is a composed video track.
type Track struct {
	ID              string
	Resolution      *normalize.Resolution
	Clips           []Clip
	FrameIntervalNS int64
	DurationNS      int64
}

// Timeline is the composed multi-track result.
type Timeline struct {
	Tracks     []*Track
	DurationNS int64
	FrameRate  float64
}

// Skip records a track excluded from composition and why.
type Skip struct {
	TrackID string
	Reason  *clock.Error
}

// Result holds a composed timeline and any excluded tracks.
type Result struct {
	Timeline *Timeline
	Skipped  []Skip
}

// Compose maps every track onto a shared origin. Clips must be non-negative,
// in non-decreasing start order, non-overlapping, and inside the declared
// source duration. A violating track is skipped with a typed error naming it;
// composition fails only when no track survives.
func Compose(src []*probe.Track, resolutions []*normalize.Resolution) (*Result, error) {
	if len(src) != len(resolutions) {
		return nil, clock.Newf(clock.KindInvalidMetadata,
			"track/resolution count mismatch: %d vs %d", len(src), len(resolutions))
	}
	res := &Result{Timeline: &Timeline{}}
	for i, t := range src {
		track, err := composeTrack(t, resolutions[i])
		if err != nil {
			res.Skipped = append(res.Skipped, Skip{TrackID: t.ID, Reason: err})
			continue
		}
		res.Timeline.Tracks = append(res.Timeline.Tracks, track)
		if track.DurationNS > res.Timeline.DurationNS {
			res.Timeline.DurationNS = track.DurationNS
		}
	}
	if len(res.Timeline.Tracks) == 0 {
		return nil, clock.New(clock.KindInvalidMetadata, "no valid tracks to compose")
	}
	sort.SliceStable(res.Timeline.Tracks, func(i, j int) bool {
		return res.Timeline.Tracks[i].ID < res.Timeline.Tracks[j].ID
	})
	return res, nil
}

func composeTrack(t *probe.Track, r *normalize.Resolution) (*Track, *clock.Error) {
	if t.DurationNS < 0 {
		return nil, clock.Newf(clock.KindNegativeDuration, "track duration %d ns is negative", t.DurationNS).
			WithTrack(t.ID)
	}
	if t.SampleIntervalNS <= 0 {
		return nil, clock.Newf(clock.KindInvalidMetadata, "frame interval %d ns is not positive", t.SampleIntervalNS).
			WithTrack(t.ID)
	}
	clips := t.Clips
	if len(clips) == 0 {
		if t.DurationNS == 0 {
			return nil, clock.New(clock.KindInvalidMetadata, "track has no clips and zero duration").
				WithTrack(t.ID)
		}
		clips = []probe.ClipRef{{StartNS: 0, DurationNS: t.DurationNS}}
	}
	out := &Track{ID: t.ID, Resolution: r, FrameIntervalNS: t.SampleIntervalNS}
	var prevEnd int64 = -1
	for i, c := range clips {
		if c.DurationNS < 0 {
			return nil, clock.Newf(clock.KindNegativeDuration, "clip %d duration %d ns is negative", i, c.DurationNS).
				WithTrack(t.ID)
		}
		if c.StartNS < 0 {
			return nil, clock.Newf(clock.KindOutOfRange, "clip %d starts below timeline origin at %d ns", i, c.StartNS).
				WithTrack(t.ID)
		}
		if i > 0 && c.StartNS < prevEnd {
			return nil, clock.Newf(clock.KindNonMonotonic,
				"clip %d starts at %d ns before previous clip end %d ns", i, c.StartNS, prevEnd).
				WithTrack(t.ID)
		}
		if t.DurationNS > 0 && c.EndNS() > t.DurationNS {
			return nil, clock.Newf(clock.KindOutOfRange,
				"clip %d ends at %d ns beyond source duration %d ns", i, c.EndNS(), t.DurationNS).
				WithTrack(t.ID)
		}
		fc := int(c.DurationNS / t.SampleIntervalNS)
		out.Clips = append(out.Clips, Clip{
			Index:      i,
			StartNS:    c.StartNS,
			DurationNS: c.DurationNS,
			FrameCount: fc,
		})
		prevEnd = c.EndNS()
	}
	out.DurationNS = prevEnd
	if out.DurationNS == 0 {
		return nil, clock.New(clock.KindInvalidMetadata, "track has zero composed duration").
			WithTrack(t.ID)
	}
	return out, nil
}

// BoundaryError returns the distance between a boundary timestamp and the
// nearest frame grid line of the given track.
func BoundaryError(ns, intervalNS int64) int64 {
	if intervalNS <= 0 {
		return ns
	}
	rem := ns % intervalNS
	if rem < 0 {
		rem = -rem
	}
	if rem > intervalNS/2 {
		rem = intervalNS - rem
	}
	return rem
}

// FrameTimes expands a track clip into frame presentation timestamps.
func FrameTimes(t *Track, c Clip) []int64 {
	if t.FrameIntervalNS <= 0 || c.FrameCount <= 0 {
		return nil
	}
	out := make([]int64, 0, c.FrameCount)
	for i := 0; i < c.FrameCount; i++ {
		out = append(out, c.StartNS+int64(i)*t.FrameIntervalNS)
	}
	return out
}

// Describe renders a one-line summary used in terminal output.
func (t *Track) Describe() string {
	return fmt.Sprintf("%s clips=%d frames=%d span=%dns", t.ID, len(t.Clips), t.FrameCount(), t.DurationNS)
}

// FrameCount is the total frames across every clip.
func (t *Track) FrameCount() int {
	n := 0
	for _, c := range t.Clips {
		n += c.FrameCount
	}
	return n
}
