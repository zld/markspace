package media

import (
	"fmt"
	"sort"

	"mediad/timeline"
)

// Rendition is one quality level of a program, backed by a parsed track.
type Rendition struct {
	// Name identifies the rendition in reports ("1080p", "720p", ...).
	Name string
	// Track is the video track that defines the rendition's segment grid.
	Track Track
}

// Segment is one aligned media segment. Start and End are exact times on the
// shared timeline; the cut at Start is always a keyframe in every rendition.
type Segment struct {
	Index     int    `json:"index"`
	Start     int64  `json:"start_us"`
	End       int64  `json:"end_us"`
	Duration  int64  `json:"duration_us"`
	Rendition string `json:"rendition"`
	// ByRendition maps each rendition to its sample index range and the max
	// boundary error (in µs) versus the master cut time.
	ByRendition map[string]RenditionSegment `json:"renditions"`
}

// RenditionSegment is one rendition's contribution to a segment.
type RenditionSegment struct {
	FirstSample int   `json:"first_sample"`
	LastSample  int   `json:"last_sample"`
	StartTicks  int64 `json:"start_ticks"`
	// BoundaryErrorUs is how far this rendition's keyframe strays from the
	// master cut, in microseconds. The acceptance limit is one video frame.
	BoundaryErrorUs int64 `json:"boundary_error_us"`
}

// AlignOptions tunes segment alignment.
type AlignOptions struct {
	// MaxSegmentUs is the target segment length. Cuts are moved to the nearest
	// keyframe so a segment is never shorter than MinSegmentUs.
	TargetSegmentUs int64
	// MaxBoundaryErrorUs is the tolerance between a rendition's keyframe and
	// the master cut. A rendition whose nearest keyframe is farther is left
	// unaligned and reported.
	MaxBoundaryErrorUs int64
}

// AlignResult is the outcome of aligning renditions.
type AlignResult struct {
	Segments []Segment
	Issues   timeline.Issues
	// Master is the rendition whose keyframes define the cut grid.
	Master string
}

// AlignSegments cuts every rendition at the master's keyframes. The master is
// the rendition with the most keyframes (ties broken by name) so the grid is
// driven by the densest timeline. For each master keyframe time, every other
// rendition must have a keyframe within MaxBoundaryErrorUs; if it does not, the
// segment is reported with the offending rendition named and the whole result
// is marked fatal so a partial manifest is never published as complete.
func AlignSegments(renditions []Rendition, opts AlignOptions) (AlignResult, error) {
	var res AlignResult
	if len(renditions) == 0 {
		return res, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeNoBoundary, Message: "no renditions to align"}}
	}
	if opts.MaxBoundaryErrorUs <= 0 {
		return res, fmt.Errorf("AlignSegments: MaxBoundaryErrorUs must be positive")
	}

	master := chooseMaster(renditions)
	res.Master = master.Name
	masterKF := keyframeTimes(master.Track)
	if len(masterKF) == 0 {
		return res, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeMissingKeyframe, File: master.Track.sourcePath,
			TrackID: master.Track.ID, Message: fmt.Sprintf("master rendition %q has no keyframes", master.Name)}}
	}

	cuts := selectCuts(masterKF, opts.TargetSegmentUs)
	for i, cut := range cuts {
		var end int64
		if i+1 < len(cuts) {
			end = cuts[i+1]
		} else {
			end = masterEndUs(master.Track)
		}
		seg := Segment{
			Index:       i,
			Start:       cut,
			End:         end,
			Duration:    end - cut,
			Rendition:   master.Name,
			ByRendition: map[string]RenditionSegment{},
		}
		for _, r := range renditions {
			rs, err := sliceRendition(r, cut, end, opts.MaxBoundaryErrorUs)
			if err != nil {
				return res, err
			}
			if rs == nil {
				res.Issues.Add(timeline.Issue{
					Severity: timeline.SeverityError,
					Code:     timeline.CodeNoBoundary,
					File:     r.Track.sourcePath,
					TrackID:  r.Track.ID,
					Message: fmt.Sprintf("segment %d: rendition %q has no keyframe within %d µs of %d µs",
						i, r.Name, opts.MaxBoundaryErrorUs, cut),
				})
				continue
			}
			seg.ByRendition[r.Name] = *rs
		}
		res.Segments = append(res.Segments, seg)
	}
	if res.Issues.HasErrors() {
		return res, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeNoBoundary,
			Message: "alignment failed: one or more renditions lack a keyframe at a cut"}}
	}
	return res, nil
}

// chooseMaster picks the rendition with the most keyframes, then the earliest
// name, so the choice does not depend on slice order.
func chooseMaster(renditions []Rendition) Rendition {
	best := renditions[0]
	bestKF := len(best.Track.Keyframes())
	for _, r := range renditions[1:] {
		n := len(r.Track.Keyframes())
		if n > bestKF || (n == bestKF && r.Name < best.Name) {
			best, bestKF = r, n
		}
	}
	return best
}

// keyframeTimes returns keyframe presentation times in microseconds, sorted and
// deduplicated. Composite time (cts) is used because that is when the frame is
// displayed, which is what cue alignment must match.
func keyframeTimes(t Track) []int64 {
	var out []int64
	for _, s := range t.Samples {
		if !s.Sync {
			continue
		}
		us := ticksToMicros(s.CompositeTicks, t.TimeScale)
		if len(out) == 0 || out[len(out)-1] != us {
			out = append(out, us)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return dedupe(out)
}

func masterEndUs(t Track) int64 {
	if len(t.Samples) == 0 {
		return 0
	}
	last := t.Samples[len(t.Samples)-1]
	return ticksToMicros(last.CompositeTicks+last.DurationTicks, t.TimeScale)
}

// selectCuts chooses a subset of keyframe times spaced near targetUs apart. The
// first keyframe is always a cut; each next cut is the first keyframe at or
// after the previous cut plus the target. This keeps segments close to the
// target without ever cutting off a keyframe.
func selectCuts(keyframes []int64, targetUs int64) []int64 {
	if len(keyframes) == 0 {
		return nil
	}
	if targetUs <= 0 {
		return append([]int64(nil), keyframes...)
	}
	cuts := []int64{keyframes[0]}
	next := keyframes[0] + targetUs
	for _, kf := range keyframes[1:] {
		if kf >= next {
			cuts = append(cuts, kf)
			next = kf + targetUs
		}
	}
	return cuts
}

// sliceRendition finds the keyframe at or nearest cut within tolerance and the
// sample range [start, end).
func sliceRendition(r Rendition, cutUs, endUs, tolUs int64) (*RenditionSegment, error) {
	t := r.Track
	bestIdx := -1
	bestErr := int64(-1)
	for _, i := range t.Keyframes() {
		us := ticksToMicros(t.Samples[i].CompositeTicks, t.TimeScale)
		err := abs64(us - cutUs)
		if err > tolUs {
			continue
		}
		if bestErr < 0 || err < bestErr {
			bestIdx, bestErr = i, err
		}
	}
	if bestIdx < 0 {
		return nil, nil
	}
	// The segment covers samples from the keyframe up to (not including) the
	// sample at or after endUs.
	last := len(t.Samples) - 1
	for i := bestIdx + 1; i < len(t.Samples); i++ {
		if ticksToMicros(t.Samples[i].CompositeTicks, t.TimeScale) >= endUs {
			last = i - 1
			break
		}
	}
	if last < bestIdx {
		last = bestIdx
	}
	return &RenditionSegment{
		FirstSample:     bestIdx,
		LastSample:      last,
		StartTicks:      t.Samples[bestIdx].CompositeTicks,
		BoundaryErrorUs: bestErr,
	}, nil
}

func ticksToMicros(ticks int64, timescale uint32) int64 {
	r, err := timeline.NewRational(ticks, int64(timescale))
	if err != nil {
		return 0
	}
	us, err := r.Micros()
	if err != nil {
		return 0
	}
	return us
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func dedupe(sorted []int64) []int64 {
	if len(sorted) == 0 {
		return nil
	}
	out := sorted[:1]
	for _, v := range sorted[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// SamplePath returns the absolute byte span of a track's sample range. It is the
// point where a manifest records exactly which bytes a segment covers.
func SamplePath(t Track, first, last int) (start, end int64, err error) {
	if first < 0 || last >= len(t.Samples) || first > last {
		return 0, 0, fmt.Errorf("sample range [%d,%d] outside track with %d samples", first, last, len(t.Samples))
	}
	return t.Samples[first].Offset, t.Samples[last].Offset + t.Samples[last].Size, nil
}
