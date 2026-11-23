package pipeline

import (
	"mediamux/manifest"
)

// MaskSegments returns a shallow copy of doc with only the segments in
// [from,to), so a report about a partially processed timeline covers exactly
// the segments that were committed.
func MaskSegments(doc *manifest.Document, from, to int) *manifest.Document {
	if doc == nil {
		return nil
	}
	if from < 0 || to > len(doc.Segments) || from > to {
		return doc
	}
	cp := *doc
	cp.Segments = append([]manifest.Segment(nil), doc.Segments[from:to]...)
	return &cp
}

// RefOf adapts a manifest document to the comparison view.
func RefOf(doc *manifest.Document) []SegmentRef {
	if doc == nil {
		return nil
	}
	out := make([]SegmentRef, len(doc.Segments))
	for i, s := range doc.Segments {
		out[i] = SegmentRef{ID: s.ID, Duration: s.Duration, DeclaredOffset: s.DeclaredOffset}
	}
	return out
}

// MediaDurationsMS extracts the analysed duration of each outcome, parallel to
// the manifest. Outcomes missing from the slice report zero and are skipped.
func MediaDurationsMS(outs []SegmentOutcome, count int) []int64 {
	out := make([]int64, count)
	for _, o := range outs {
		if o.Index >= 0 && o.Index < count {
			out[o.Index] = o.Quality.DurationMS
		}
	}
	return out
}
