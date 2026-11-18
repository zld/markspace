// Package timeline normalizes every rendition onto one common integer
// timescale, applies presentation offsets and builds SegmentTimeline runs.
package timeline

import (
	"fmt"
	"sort"

	"dashforge/internal/model"
)

// BoundaryDeviation records one boundary whose millisecond timestamp could not
// be represented exactly on the chosen timescale. The integer timeline still
// keeps the nearest exact integer tick; ErrorMs quantifies the rounding.
type BoundaryDeviation struct {
	Rendition     string  `json:"rendition"`
	SegmentNumber int     `json:"segment_number"`
	TimestampMs   int64   `json:"timestamp_ms"`
	ErrorMs       float64 `json:"error_ms"`
}

// Report summarizes normalization. Deviations are ordered by rendition then
// segment number so output is reproducible.
type Report struct {
	Timescale  int                 `json:"timescale"`
	Deviations []BoundaryDeviation `json:"deviations"`
}

// Normalize converts validated metadata into the shared Manifest model.
// Renditions are sorted by strictly increasing bandwidth; fragment timestamps
// are expressed in timescale units measured from the common period origin.
func Normalize(meta *model.Metadata, timescale int) (*model.Manifest, *Report, error) {
	if timescale <= 0 {
		return nil, nil, fmt.Errorf("timescale must be positive, got %d", timescale)
	}
	man := &model.Manifest{Title: meta.Title, Timescale: timescale}
	report := &Report{Timescale: timescale}

	for i := range meta.Renditions {
		rd := &meta.Renditions[i]
		rm, devs, err := normalizeRendition(rd, timescale)
		if err != nil {
			return nil, nil, fmt.Errorf("rendition %q: %w", rd.ID, err)
		}
		report.Deviations = append(report.Deviations, devs...)
		man.Renditions = append(man.Renditions, rm)
	}

	sort.SliceStable(man.Renditions, func(i, j int) bool {
		return man.Renditions[i].Bandwidth < man.Renditions[j].Bandwidth
	})
	for i := 1; i < len(man.Renditions); i++ {
		prev, cur := man.Renditions[i-1], man.Renditions[i]
		if cur.Bandwidth == prev.Bandwidth {
			return nil, nil, fmt.Errorf("renditions %q and %q share bandwidth %d; bandwidth must be strictly increasing",
				prev.ID, cur.ID, cur.Bandwidth)
		}
	}
	sort.SliceStable(report.Deviations, func(i, j int) bool {
		if report.Deviations[i].Rendition != report.Deviations[j].Rendition {
			return report.Deviations[i].Rendition < report.Deviations[j].Rendition
		}
		return report.Deviations[i].SegmentNumber < report.Deviations[j].SegmentNumber
	})
	return man, report, nil
}

func normalizeRendition(rd *model.Rendition, timescale int) (*model.RenditionManifest, []BoundaryDeviation, error) {
	segs := make([]model.Segment, len(rd.Segments))
	copy(segs, rd.Segments)
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })

	offset, offErr := scale(rd.StartOffsetMs, timescale)
	rm := &model.RenditionManifest{
		ID:             rd.ID,
		Width:          rd.Width,
		Height:         rd.Height,
		Bandwidth:      rd.Bandwidth,
		Codec:          rd.Codec,
		FrameRate:      rd.FrameRate,
		Timescale:      timescale,
		StartOffset:    offset,
		Initialization: rd.Initialization,
		Media:          rd.Media,
		SegmentRoot:    rd.SegmentRoot,
	}
	var devs []BoundaryDeviation
	if offErr != 0 {
		devs = append(devs, BoundaryDeviation{
			Rendition: rd.ID, SegmentNumber: 0,
			TimestampMs: rd.StartOffsetMs, ErrorMs: offErr,
		})
	}

	for _, seg := range segs {
		start, sErr := scale(seg.StartMs, timescale)
		dur, dErr := scale(seg.DurationMs, timescale)
		if dur <= 0 {
			return nil, nil, fmt.Errorf("segment %d collapses to zero duration at timescale %d", seg.Number, timescale)
		}
		rm.Segments = append(rm.Segments, model.SegmentInfo{
			Number:   seg.Number,
			Start:    offset + start,
			Duration: dur,
		})
		if sErr != 0 {
			devs = append(devs, BoundaryDeviation{
				Rendition: rd.ID, SegmentNumber: seg.Number,
				TimestampMs: seg.StartMs, ErrorMs: sErr,
			})
		}
		if dErr != 0 {
			devs = append(devs, BoundaryDeviation{
				Rendition: rd.ID, SegmentNumber: seg.Number,
				TimestampMs: seg.StartMs + seg.DurationMs, ErrorMs: dErr,
			})
		}
	}

	rm.Timeline = buildTimeline(rm.Segments)
	if len(rm.Segments) > 0 {
		last := rm.Segments[len(rm.Segments)-1]
		rm.Duration = last.Start + last.Duration
	}
	return rm, devs, nil
}

// scale converts a millisecond timestamp to integer timescale ticks,
// rounding to the nearest tick. The returned error is the absolute rounding
// difference in milliseconds (0 when the timestamp divides exactly).
func scale(ms int64, timescale int) (int64, float64) {
	num := ms * int64(timescale)
	rounded := (num + 500) / 1000
	if num < 0 && num%1000 != 0 { // defensive: inputs are validated non-negative
		rounded = (num - 500) / 1000
	}
	errMs := float64(0)
	if num != rounded*1000 {
		errMs = absFloat(float64(rounded)*1000/float64(timescale) - float64(ms))
	}
	return rounded, errMs
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// buildTimeline coalesces contiguous, equal-duration fragments into S runs
// carrying an r repeat count. A short final fragment naturally opens its own
// run with repeat 0.
func buildTimeline(segs []model.SegmentInfo) []model.TimelineEntry {
	var entries []model.TimelineEntry
	for _, seg := range segs {
		if n := len(entries); n > 0 {
			last := &entries[n-1]
			expected := last.Start + last.Duration*int64(last.Repeat+1)
			if seg.Start == expected && seg.Duration == last.Duration {
				last.Repeat++
				continue
			}
		}
		entries = append(entries, model.TimelineEntry{
			Start: seg.Start, Duration: seg.Duration, Repeat: 0,
		})
	}
	return entries
}
