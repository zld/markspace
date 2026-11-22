package stream

import (
	"sort"

	"mediaarchive/internal/diag"
)

// Segment is a locatable damaged range on one source. Media times are
// milliseconds on the unified base; offsets refer to the same source as the
// faults. Segments from one Classify call never overlap.
type Segment struct {
	Source      string
	Code        string // fault family: subtitle, rtp, mp4, vfr
	StartMs     float64
	EndMs       float64
	StartOffset int64
	EndOffset   int64
	FaultCount  int
	Recoverable bool
	Detail      string
	// Codes lists the distinct fault codes folded into this segment.
	Codes []string
}

// ClassifyOptions controls how far apart two faults may be while still
// belonging to one segment.
type ClassifyOptions struct {
	Source      string
	TimeMergeMs float64
	ByteMerge   int64
	// TimeKnown marks whether MediaMs on faults is meaningful for this source.
	TimeKnown bool
}

// Classify groups faults by family into non-overlapping segments. Ordering is
// deterministic: faults sort by offset then media time, and segments sort by
// start time, offset and family, so identical input always yields identical
// segments.
func Classify(faults []*diag.Fault, opts ClassifyOptions) []Segment {
	if opts.TimeMergeMs <= 0 {
		opts.TimeMergeMs = 250
	}
	if opts.ByteMerge <= 0 {
		opts.ByteMerge = 1 << 16
	}
	cp := make([]*diag.Fault, len(faults))
	copy(cp, faults)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].Offset != cp[j].Offset {
			return cp[i].Offset < cp[j].Offset
		}
		return cp[i].MediaMs < cp[j].MediaMs
	})

	// Work per family so an interleaved stream of subtitle and RTP faults
	// cannot merge unrelated damage.
	type key struct{ fam string }
	buckets := map[key][]*diag.Fault{}
	var order []key
	for _, f := range cp {
		k := key{family(f.Code)}
		if _, ok := buckets[k]; !ok {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], f)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].fam < order[j].fam })

	var segs []Segment
	for _, k := range order {
		var cur *Segment
		for _, f := range buckets[k] {
			start, end := 0.0, 0.0
			if f.HasMedia && opts.TimeKnown {
				start, end = f.MediaMs, f.MediaMs
			}
			if cur != nil {
				closeOffset := f.Offset-cur.EndOffset <= opts.ByteMerge
				closeTime := !opts.TimeKnown || !f.HasMedia ||
					(start-cur.EndMs) <= opts.TimeMergeMs
				if closeOffset && closeTime && start >= cur.StartMs-1e-9 {
					cur.EndOffset = f.Offset
					if end > cur.EndMs {
						cur.EndMs = end
					}
					cur.FaultCount++
					cur.Codes = appendCode(cur.Codes, f.Code)
					cur.Recoverable = cur.Recoverable && recoverable(f.Code)
					continue
				}
			}
			segs = append(segs, Segment{
				Source: opts.Source, Code: k.fam, StartMs: start, EndMs: end,
				StartOffset: f.Offset, EndOffset: f.Offset, FaultCount: 1,
				Recoverable: recoverable(f.Code), Detail: f.Detail,
				Codes: []string{f.Code},
			})
			cur = &segs[len(segs)-1]
		}
	}
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].StartMs != segs[j].StartMs {
			return segs[i].StartMs < segs[j].StartMs
		}
		if segs[i].StartOffset != segs[j].StartOffset {
			return segs[i].StartOffset < segs[j].StartOffset
		}
		return segs[i].Code < segs[j].Code
	})
	return segs
}

func appendCode(codes []string, code string) []string {
	for _, c := range codes {
		if c == code {
			return codes
		}
	}
	return append(codes, code)
}

// Validate enforces manifest invariants: segments on one source are sorted
// and never overlap (by offset or by media time).
func Validate(segs []Segment) (overlaps int, unsorted bool) {
	for i := 1; i < len(segs); i++ {
		prev, cur := segs[i-1], segs[i]
		if cur.StartOffset < prev.StartOffset {
			unsorted = true
		}
		if cur.StartOffset <= prev.EndOffset && cur.StartMs < prev.EndMs {
			overlaps++
		}
	}
	return overlaps, unsorted
}

// SplitBySource partitions a manifest's segments for per-source validation.
func SplitBySource(segs []Segment) map[string][]Segment {
	out := map[string][]Segment{}
	for _, s := range segs {
		out[s.Source] = append(out[s.Source], s)
	}
	return out
}

func family(code string) string {
	switch code {
	case diag.CodeInvalidTime, diag.CodeTruncated, diag.CodeAmbiguousDF,
		diag.CodeTimestampRollback, diag.CodeMissingBase:
		return "subtitle"
	case diag.CodeRTPSegmentGap, diag.CodeRTPDuplicate, diag.CodeRTPBurst:
		return "rtp"
	case diag.CodeMP4Truncated, diag.CodeMP4Structure:
		return "mp4"
	case diag.CodeVFRGap:
		return "vfr"
	default:
		return "other"
	}
}

// recoverable distinguishes damage whose remaining bytes can still be cited
// (late RTP fills, resynchronised subtitles, isolated VFR holds) from damage
// where no row may be written from that range at all (truncated media).
func recoverable(code string) bool {
	switch code {
	case diag.CodeQueueFull, diag.CodeCanceled, diag.CodeMP4Truncated, diag.CodeMP4Structure:
		return false
	default:
		return true
	}
}
