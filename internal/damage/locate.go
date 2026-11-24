// Package damage localization turns pre-scan truncations, undecidable
// indexes and missing samples into timeline ranges with the shared error
// model. Ranges are half-open nanosecond intervals and carry a
// deterministic flag: index-derived tail ranges are explicitly inferred.
package damage

import (
	"fmt"
	"sort"

	"mediacheck/internal/container"
	"mediacheck/internal/media"
)

// Locator binds container scan output and per-track timelines and produces
// unified Error records with nanosecond ranges.
type Locator struct {
	lines   map[uint32]*media.TrackLine
	lineIDs []uint32
}

// NewLocator constructs a Locator from the track timelines built by the
// pipeline. Tracks without a timeline are still locatable via byte-level
// truncation records.
func NewLocator(lines []*media.TrackLine) *Locator {
	l := &Locator{lines: map[uint32]*media.TrackLine{}}
	for _, line := range lines {
		l.lines[line.TrackID] = line
		l.lineIDs = append(l.lineIDs, line.TrackID)
	}
	sort.Slice(l.lineIDs, func(i, j int) bool { return l.lineIDs[i] < l.lineIDs[j] })
	return l
}

// LocateFromScan converts every structural defect the container scan found
// into a damage.Error with a timeline range. It never returns nil for real
// defects; callers decide recoverability downstream.
func (l *Locator) LocateFromScan(res *container.ScanResult) []Error {
	var out []Error
	truncByTrack := map[uint32]bool{}

	// 1. Pre-scan byte-level truncations. A truncation whose box path sits
	// inside a trak is attributed to that track; top-level cuts are
	// container faults with track ID 0.
	for _, rec := range res.Pre.Truncations {
		trackID := l.trackForBox(res, rec)
		if trackID != 0 {
			truncByTrack[trackID] = true
		}
		out = append(out, l.makeTruncation(res, trackID, rec.Type, rec.Offset))
	}

	// 2. Per-index undecidable / truncated-payload tables.
	for i := range res.Tracks {
		tm := &res.Tracks[i]
		idx := tm.Index
		if !idx.Undecidable {
			continue
		}
		rng, ok := l.tailRange(tm.TrackID)
		if !ok {
			rng = Range{Deterministic: false}
		}
		rng.Deterministic = false
		out = append(out, Error{
			Code:        CodeIndexUndecidable,
			TrackID:     tm.TrackID,
			Range:       rng,
			Detail:      idx.Reason,
			Recoverable: RecoverableUnknown,
		})
	}

	// 3. Missing samples: stsz declares more samples than stts materialized.
	for i := range res.Tracks {
		tm := &res.Tracks[i]
		declared := tm.Index.SampleCount
		have := uint32(len(tm.RawSamples))
		if declared > have {
			rng, _ := l.tailRange(tm.TrackID)
			rng.Deterministic = false
			out = append(out, Error{
				Code:    CodeMissingSample,
				TrackID: tm.TrackID,
				Range:   rng,
				Detail: fmt.Sprintf(
					"sample table declares %d samples but timing covers %d",
					declared, have),
				Recoverable: RecoverableUnknown,
			})
		}
	}
	return Merge(out)
}

// makeTruncation builds one truncated_box error, bounding it with the track
// tail range when the track timeline is known.
func (l *Locator) makeTruncation(res *container.ScanResult, trackID uint32, boxType string, offset int64) Error {
	rng := Range{Deterministic: false}
	if trackID != 0 {
		if r, ok := l.tailRange(trackID); ok {
			rng = r
		}
	}
	return Error{
		Code:        CodeTruncatedBox,
		TrackID:     trackID,
		Range:       rng,
		Detail:      "box " + boxType + " truncated at byte " + itoa64(offset),
		Recoverable: RecoverableUnknown,
	}
}

// tailRange returns the half-open range from the last known point to the
// track end. Returns false when the track has no timeline.
func (l *Locator) tailRange(trackID uint32) (Range, bool) {
	line, ok := l.lines[trackID]
	if !ok || len(line.Points) == 0 {
		return Range{}, false
	}
	last := line.Points[len(line.Points)-1]
	return Range{
		StartNanos:    last.StartNanos,
		EndNanos:      last.StartNanos + last.DurationNanos,
		Deterministic: last.Source == media.SourceDecoded,
	}, true
}

// trackForBox maps a truncated box record onto the track whose trak box
// contains it, using byte offsets from the pre-scan. Returns 0 for
// top-level/container truncations.
func (l *Locator) trackForBox(res *container.ScanResult, rec container.BoxRecord) uint32 {
	var best uint32
	bestEnd := int64(-1)
	for _, b := range res.Pre.Boxes {
		if b.Type != "trak" {
			continue
		}
		end := b.Offset + int64(b.Size)
		if rec.Offset >= b.Offset && rec.Offset < end && b.Offset <= rec.Offset {
			// Smallest enclosing trak wins; tie-break by earliest offset.
			if best == 0 || end < bestEnd {
				best = l.trackIDAtOffset(res, b.Offset)
				bestEnd = end
			}
		}
	}
	return best
}

// trackIDAtOffset resolves a trak box offset to its declared track ID by
// matching scan output order against trak offsets.
func (l *Locator) trackIDAtOffset(res *container.ScanResult, trakOffset int64) uint32 {
	trakOffsets := make([]int64, 0)
	idByOffset := map[int64]uint32{}
	for _, b := range res.Pre.Boxes {
		if b.Type == "trak" {
			trakOffsets = append(trakOffsets, b.Offset)
		}
	}
	sort.Slice(trakOffsets, func(i, j int) bool { return trakOffsets[i] < trakOffsets[j] })
	for i, tm := range res.Tracks {
		if i < len(trakOffsets) {
			idByOffset[trakOffsets[i]] = tm.TrackID
		}
	}
	return idByOffset[trakOffset]
}

// Merge coalesces overlapping ranges that share the same code and track,
// preserving deterministic=false whenever any constituent is inferred.
func Merge(errs []Error) []Error {
	if len(errs) == 0 {
		return nil
	}
	sorted := append([]Error(nil), errs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].TrackID != sorted[j].TrackID {
			return sorted[i].TrackID < sorted[j].TrackID
		}
		if sorted[i].Code != sorted[j].Code {
			return sorted[i].Code < sorted[j].Code
		}
		return sorted[i].Range.StartNanos < sorted[j].Range.StartNanos
	})
	out := make([]Error, 0, len(sorted))
	for _, e := range sorted {
		if len(out) == 0 || !sameMergeKey(out[len(out)-1], e) ||
			!out[len(out)-1].Range.Overlaps(e.Range) && e.Range.StartNanos > out[len(out)-1].Range.EndNanos {
			out = append(out, e)
			continue
		}
		last := &out[len(out)-1]
		if e.Range.EndNanos > last.Range.EndNanos {
			last.Range.EndNanos = e.Range.EndNanos
		}
		last.Range.Deterministic = last.Range.Deterministic && e.Range.Deterministic
	}
	return out
}

func sameMergeKey(a, b Error) bool {
	return a.TrackID == b.TrackID && a.Code == b.Code
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
