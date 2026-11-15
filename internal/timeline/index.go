// Package timeline normalizes parsed fragments from several bitrates onto a
// single integer-millisecond axis and verifies that their time lines agree.
//
// All conversions use integer scale factors; there is deliberately no
// floating-point arithmetic anywhere in the time path, so long runs cannot
// accumulate rounding error.
package timeline

import (
	"errors"
	"fmt"
	"sort"

	"hlsforge/internal/ingest"
	"hlsforge/internal/report"
)

// ErrOverflow means a timescale conversion would overflow int64.
var ErrOverflow = errors.New("timeline: integer overflow in time conversion")

// msScale is the target timescale of every normalized value: one unit is one
// millisecond.
const msScale int64 = 1000

// Part is one part-sized timeline slot. Missing is set when no fragment
// covers the slot; GAP slots keep their position instead of shifting later
// content forward.
type Part struct {
	Seq        int // fragment/part sequence number (1-based, contiguous)
	StartMs    int64
	DurationMs int64
	Fragment   *ingest.Fragment // nil for a gap slot
	Missing    bool
}

// Track is the normalized view of one bitrate.
type Track struct {
	Bitrate    int
	Timescale  uint32
	FromSeq    int
	ToSeq      int
	PartMs     int64
	Parts      []Part
	StartMs    int64 // earliest non-gap start
	EndMs      int64 // latest end
	HasContent bool
}

// scaleToMs converts value units expressed in srcScale to integer ms.
func scaleToMs(value, srcScale int64) (int64, error) {
	if srcScale <= 0 {
		return 0, fmt.Errorf("timeline: invalid timescale %d", srcScale)
	}
	if value > (1<<62)/msScale {
		return 0, fmt.Errorf("%w: value %d in scale %d",
			ErrOverflow, value, srcScale)
	}
	return value * msScale / srcScale, nil
}

// Build turns an ingested track into ordered, fixed-size timeline parts.
func Build(in *ingest.TrackIngest) (*Track, error) {
	if in == nil {
		return nil, fmt.Errorf("timeline: nil track")
	}
	src := int64(in.Timescale)
	t := &Track{
		Bitrate: in.Bitrate, Timescale: in.Timescale,
		FromSeq: in.FromSeq, ToSeq: in.ToSeq, PartMs: in.PartMs,
	}
	for seq := in.FromSeq; seq <= in.ToSeq; seq++ {
		slotStart := int64(seq-in.FromSeq) * in.PartMs
		f := in.Slots[seq]
		if f == nil {
			t.Parts = append(t.Parts, Part{
				Seq: seq, StartMs: slotStart, DurationMs: in.PartMs, Missing: true,
			})
			continue
		}
		start, err := scaleToMs(int64(f.BaseTime), src)
		if err != nil {
			return nil, err
		}
		dur, err := scaleToMs(int64(f.Duration), src)
		if err != nil {
			return nil, err
		}
		if dur <= 0 {
			return nil, fmt.Errorf("timeline: fragment %d has non-positive duration", seq)
		}
		t.Parts = append(t.Parts, Part{
			Seq: seq, StartMs: start, DurationMs: dur, Fragment: f,
		})
		if !t.HasContent || start < t.StartMs {
			t.StartMs = start
		}
		if end := start + dur; end > t.EndMs {
			t.EndMs = end
		}
		t.HasContent = true
	}
	// Sort by declared start time while keeping gap slots anchored by seq.
	sort.SliceStable(t.Parts, func(i, j int) bool {
		return t.Parts[i].StartMs < t.Parts[j].StartMs
	})
	return t, nil
}

// Deviation compares one track against a reference track and returns the
// maximum absolute start/end offset in milliseconds.
func Deviation(ref, other *Track) int64 {
	if !ref.HasContent || !other.HasContent {
		return 0
	}
	d := abs(ref.StartMs - other.StartMs)
	if e := abs(ref.EndMs - other.EndMs); e > d {
		d = e
	}
	return d
}

// Compare runs cross-bitrate drift checks. The highest-bitrate content-bearing
// track is the reference (the drift fault is injected into lower renditions).
// Every other track whose deviation exceeds toleranceMs yields a
// TIMELINE_DRIFT finding pinned to that bitrate.
func Compare(tracks []*Track, toleranceMs int64) ([]report.Finding, int64) {
	var ref *Track
	for _, t := range tracks {
		if !t.HasContent {
			continue
		}
		if ref == nil || t.Bitrate > ref.Bitrate {
			ref = t
		}
	}
	if ref == nil {
		return nil, 0
	}
	var findings []report.Finding
	var maxDev int64
	for _, t := range tracks {
		if !t.HasContent {
			continue
		}
		dev := Deviation(ref, t)
		if dev > maxDev {
			maxDev = dev
		}
		if dev > toleranceMs {
			findings = append(findings, report.Finding{
				Code: report.CodeTimelineDrift, Bitrate: t.Bitrate,
				Detail: fmt.Sprintf("timeline deviates %dms from reference, tolerance %dms",
					dev, toleranceMs),
			})
		}
	}
	return findings, maxDev
}

// GapCount returns how many part slots were never filled.
func (t *Track) GapCount() int {
	n := 0
	for _, p := range t.Parts {
		if p.Missing {
			n++
		}
	}
	return n
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
