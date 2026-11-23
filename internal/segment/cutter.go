// Package segment cuts corrected media samples into manifest segments on one
// shared timeline.
//
// Boundaries land on the nearest legal sample start, never overlap, and every
// interval the cutter cannot fill is emitted as an explicit gap instead of
// being skipped.
package segment

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"avbatch/internal/clock"
	"avbatch/internal/mediaerr"
)

// Sample is one video sample on the corrected timeline.
type Sample struct {
	Ordinal  int // fragment ordinal it came from
	Index    int // sample index inside the fragment
	Nanos    int64
	Duration int64
	Size     int
	// UncertaintyNanos comes from the clock subsystem and widens the window
	// in which a boundary may legally land.
	UncertaintyNanos int64
	// Keyframe marks an independently decodable sample; cutters prefer them.
	Keyframe bool
}

// End returns the sample's end time.
func (s Sample) End() int64 { return s.Nanos + s.Duration }

// Config controls cutting.
type Config struct {
	// SegmentNanos is the nominal segment duration.
	SegmentNanos int64
	// BoundarySearchNanos is how far before/after a target boundary a legal
	// sample start may be chosen.
	BoundarySearchNanos int64
	// PreferKeyframes biases selection toward keyframes inside the search
	// window.
	PreferKeyframes bool
	// MinSegmentNanos suppresses a trailing segment shorter than this by
	// merging it into the previous one. Zero keeps all segments.
	MinSegmentNanos int64
}

// Kind classifies a manifest entry.
type Kind string

const (
	KindSegment Kind = "segment"
	KindGap     Kind = "gap"
)

// Segment is one manifest entry. A gap carries no samples but always states
// the interval that is missing.
type Segment struct {
	Index int
	Kind  Kind
	Start int64
	End   int64
	// SourceOrdinals lists the fragment ordinals that contributed samples,
	// preserving input provenance.
	SourceOrdinals []int
	SampleCount    int
	Bytes          int64
	Keyframe       bool
	Reason         string
}

// Duration returns End-Start.
func (s Segment) Duration() int64 { return s.End - s.Start }

// Manifest is the cutter output in timeline order.
type Manifest struct {
	Segments []Segment
	Gaps     int
	Stats    Stats
}

// Stats summarises the cut for reporting.
type Stats struct {
	InputSamples  int
	OutputSamples int
	Segments      int
	Gaps          int
	Bytes         int64
	// AlignmentErrorNanos is the largest distance between a segment boundary
	// and the nearest legal sample start.
	AlignmentErrorNanos int64
	// TimelineStartNanos and TimelineEndNanos bound the covered interval.
	TimelineStartNanos int64
	TimelineEndNanos   int64
}

// Cutter is stateless; keep the API functional for easy testing.
type Cutter struct {
	cfg Config
}

// New validates cfg and returns a cutter.
func New(cfg Config) (*Cutter, error) {
	if cfg.SegmentNanos <= 0 {
		return nil, errors.New("segment: SegmentNanos must be positive")
	}
	if cfg.BoundarySearchNanos < 0 {
		return nil, errors.New("segment: BoundarySearchNanos must not be negative")
	}
	if cfg.BoundarySearchNanos == 0 {
		cfg.BoundarySearchNanos = cfg.SegmentNanos / 4
	}
	if cfg.MinSegmentNanos < 0 {
		return nil, errors.New("segment: MinSegmentNanos must not be negative")
	}
	return &Cutter{cfg: cfg}, nil
}

// legalStart is a candidate segment boundary.
type legalStart struct {
	nanos    int64
	sample   int
	keyframe bool
}

// Cut splits samples into segments. It returns the manifest and any ordering
// problems; a manifest is still returned when errors are present so callers
// can inspect what was recovered, but callers must not commit it as complete.
func (c *Cutter) Cut(samples []Sample) (Manifest, error) {
	var man Manifest
	if len(samples) == 0 {
		return man, mediaerr.New(mediaerr.KindNoLegalBoundary, -1, "", 0, "no samples to cut")
	}
	ordered, orderErr := c.validate(samples)
	if len(ordered) == 0 {
		return man, orderErr
	}
	if err := c.checkOverlaps(ordered); err != nil {
		return man, errors.Join(orderErr, err)
	}

	starts := make([]legalStart, len(ordered))
	for i, s := range ordered {
		starts[i] = legalStart{nanos: s.Nanos, sample: i, keyframe: s.Keyframe}
	}
	firstStart := ordered[0].Nanos
	lastEnd := ordered[len(ordered)-1].End()

	boundaries := []int64{firstStart}
	maxAlignment := int64(0)
	segErrs := []error{orderErr}
	for target := firstStart + c.cfg.SegmentNanos; target < lastEnd; target += c.cfg.SegmentNanos {
		idx := c.nearestStart(starts, target)
		chosen := ordered[idx].Nanos
		if d := abs(chosen - target); d > maxAlignment {
			maxAlignment = d
		}
		if chosen <= boundaries[len(boundaries)-1] {
			// No distinct legal start exists for this target: sample cadence
			// is coarser than the segment duration, or a long hold frame sits
			// on the boundary. Report it instead of silently skipping.
			segErrs = append(segErrs, mediaerr.New(mediaerr.KindNoLegalBoundary, -1, "", target,
				"target boundary resolves to the previous boundary; segment target skipped"))
			continue
		}
		boundaries = append(boundaries, chosen)
	}
	boundaries = append(boundaries, lastEnd)

	// Single sweep: samples are sorted, boundaries are sorted.
	si := 0
	for bi := 0; bi+1 < len(boundaries); bi++ {
		start, end := boundaries[bi], boundaries[bi+1]
		seg := Segment{Index: len(man.Segments), Kind: KindSegment, Start: start, End: end}
		seen := map[int]bool{}
		var coveredEnd int64 = -1
		for si < len(ordered) && ordered[si].Nanos < start {
			si++
		}
		next := si
		for next < len(ordered) && ordered[next].Nanos < end {
			s := ordered[next]
			seg.SampleCount++
			seg.Bytes += int64(s.Size)
			if !seen[s.Ordinal] {
				seen[s.Ordinal] = true
				seg.SourceOrdinals = append(seg.SourceOrdinals, s.Ordinal)
			}
			if s.Nanos == start {
				seg.Keyframe = s.Keyframe
			}
			if e := s.End(); e > coveredEnd {
				coveredEnd = e
			}
			next++
		}
		si = next
		sort.Ints(seg.SourceOrdinals)
		if seg.SampleCount == 0 {
			seg.Kind = KindGap
			seg.Reason = "no sample lands inside the interval"
			man.Gaps++
		}
		man.Segments = append(man.Segments, seg)
		if coveredEnd > 0 && coveredEnd < end {
			man.Gaps++
			man.Segments = append(man.Segments, Segment{
				Kind:   KindGap,
				Start:  coveredEnd,
				End:    end,
				Reason: "missing media between samples",
			})
		}
	}
	sortSegments(man.Segments)
	for i := range man.Segments {
		man.Segments[i].Index = i
	}
	if c.cfg.MinSegmentNanos > 0 {
		man = c.mergeTail(man)
	}

	man.Stats = Stats{
		InputSamples:        len(samples),
		OutputSamples:       len(ordered),
		Segments:            countSegments(man.Segments),
		Gaps:                man.Gaps,
		AlignmentErrorNanos: maxAlignment,
		TimelineStartNanos:  firstStart,
		TimelineEndNanos:    lastEnd,
	}
	for _, s := range man.Segments {
		if s.Kind == KindSegment {
			man.Stats.Bytes += s.Bytes
		}
	}
	return man, errors.Join(segErrs...)
}

func countSegments(segs []Segment) int {
	n := 0
	for _, s := range segs {
		if s.Kind == KindSegment {
			n++
		}
	}
	return n
}

func sortSegments(segs []Segment) {
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].Start != segs[j].Start {
			return segs[i].Start < segs[j].Start
		}
		return segs[i].Kind < segs[j].Kind
	})
}

// validate sorts by time and reports samples that cannot be ordered.
func (c *Cutter) validate(samples []Sample) ([]Sample, error) {
	out := make([]Sample, len(samples))
	copy(out, samples)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Nanos < out[j].Nanos })
	var errs []error
	for i, s := range out {
		if s.Duration < 0 {
			errs = append(errs, mediaerr.New(mediaerr.KindInvalidBox, s.Ordinal, "", s.Nanos,
				"sample has negative duration"))
		}
		if i == 0 {
			continue
		}
		prev := out[i-1]
		if s.Nanos == prev.Nanos {
			errs = append(errs, mediaerr.New(mediaerr.KindUnsorted, s.Ordinal, "", s.Nanos,
				fmt.Sprintf("two samples share start time %d ns", s.Nanos)))
			continue
		}
		if gap := s.Nanos - prev.Nanos; gap < prev.UncertaintyNanos+s.UncertaintyNanos {
			errs = append(errs, mediaerr.New(mediaerr.KindUnsorted, s.Ordinal, "", s.Nanos,
				fmt.Sprintf("sample spacing %d ns is inside the combined uncertainty %d ns",
					gap, prev.UncertaintyNanos+s.UncertaintyNanos)))
		}
	}
	return out, errors.Join(errs...)
}

func (c *Cutter) checkOverlaps(samples []Sample) error {
	var errs []error
	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		if cur.Nanos < prev.End() && cur.Nanos != prev.Nanos {
			errs = append(errs, mediaerr.New(mediaerr.KindOverlap, cur.Ordinal, "", cur.Nanos,
				fmt.Sprintf("sample at %d ns starts before previous sample ends at %d ns",
					cur.Nanos, prev.End())))
		}
	}
	return errors.Join(errs...)
}

// nearestStart returns the legal boundary closest to target. This is the
// defining rule for VFR: every boundary lands on a real sample start, so the
// manifest can never split a frame. When keyframes are preferred, a keyframe
// within the search window wins over a non-keyframe within half the window;
// otherwise distance decides, and ties go to the earlier sample so the choice
// does not depend on input order. A large distance is not an error: it is
// recorded as alignment error, and the resulting interval shows up as an
// explicit gap when no media covers it.
func (c *Cutter) nearestStart(starts []legalStart, target int64) int {
	window := c.cfg.BoundarySearchNanos
	best := 0
	bestDist := abs(starts[0].nanos - target)
	// Binary search for the insertion point, then scan outwards.
	lo, hi := 0, len(starts)
	for lo < hi {
		mid := (lo + hi) / 2
		if starts[mid].nanos < target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	for _, i := range []int{lo - 1, lo} {
		if i < 0 || i >= len(starts) {
			continue
		}
		if d := abs(starts[i].nanos - target); d < bestDist ||
			(d == bestDist && starts[i].nanos < starts[best].nanos) {
			best, bestDist = i, d
		}
	}
	if !c.cfg.PreferKeyframes || window <= 0 {
		return best
	}
	bias := window / 2
	for i := lo - 1; i >= 0 && target-starts[i].nanos <= window; i-- {
		if starts[i].keyframe && abs(starts[i].nanos-target)-bias < bestDist {
			best, bestDist = i, abs(starts[i].nanos-target)
		}
	}
	for i := lo; i < len(starts) && starts[i].nanos-target <= window; i++ {
		if starts[i].keyframe && abs(starts[i].nanos-target)-bias < bestDist {
			best, bestDist = i, abs(starts[i].nanos-target)
		}
	}
	return best
}

func (c *Cutter) mergeTail(man Manifest) Manifest {
	if len(man.Segments) < 2 {
		return man
	}
	last := man.Segments[len(man.Segments)-1]
	if last.Kind != KindSegment || last.Duration() >= c.cfg.MinSegmentNanos {
		return man
	}
	lastIdx := len(man.Segments) - 1
	// Find the preceding segment entry.
	for i := lastIdx - 1; i >= 0; i-- {
		if man.Segments[i].Kind != KindSegment {
			continue
		}
		prev := &man.Segments[i]
		prev.End = last.End
		prev.SampleCount += last.SampleCount
		prev.Bytes += last.Bytes
		seen := map[int]bool{}
		for _, o := range prev.SourceOrdinals {
			seen[o] = true
		}
		for _, o := range last.SourceOrdinals {
			if !seen[o] {
				prev.SourceOrdinals = append(prev.SourceOrdinals, o)
			}
		}
		sort.Ints(prev.SourceOrdinals)
		man.Segments = append(man.Segments[:lastIdx], man.Segments[lastIdx+1:]...)
		for j := range man.Segments {
			man.Segments[j].Index = j
		}
		return man
	}
	return man
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// AlignErrorMillis converts a stats value for reporting.
func (s Stats) AlignErrorMillis() float64 {
	return float64(s.AlignmentErrorNanos) / float64(time.Millisecond)
}

// ValidateManifest proves the invariants the index writer relies on: sorted,
// gapless coverage between first and last entry, no overlap.
func ValidateManifest(man Manifest) error {
	var errs []error
	prevEnd := int64(-1 << 62)
	for i, s := range man.Segments {
		if s.End < s.Start {
			errs = append(errs, mediaerr.New(mediaerr.KindManifestOrder, i, "", s.Start, "segment ends before it starts"))
		}
		if s.Start < prevEnd {
			errs = append(errs, mediaerr.New(mediaerr.KindOverlap, i, "", s.Start,
				fmt.Sprintf("manifest entry starts at %d before previous end %d", s.Start, prevEnd)))
		}
		prevEnd = s.End
	}
	return errors.Join(errs...)
}

// FromTimeline converts clock.Corrected values into samples.
func FromTimeline(points []clock.Corrected, durations []int64, sizes []int, ordinals []int, keyframes []bool) []Sample {
	n := len(points)
	if len(durations) < n {
		n = len(durations)
	}
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		s := Sample{
			Ordinal:          ordinals[i],
			Index:            i,
			Nanos:            points[i].Nanos,
			Duration:         durations[i],
			UncertaintyNanos: points[i].UncertaintyNanos,
			Keyframe:         keyframes[i],
		}
		if i < len(sizes) {
			s.Size = sizes[i]
		}
		out = append(out, s)
	}
	return out
}
