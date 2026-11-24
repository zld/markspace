package fingerprint

import "mediapipe/internal/model"

// Deduper compares segment window fingerprints and retains source mappings.
type Deduper struct {
	TimeToleranceMs int64
	MinWindows      int

	seen      []seenSegment
	Mappings  []model.DedupeMapping
	Duplicate int
}

type seenSegment struct {
	Segment      model.Segment
	Fingerprints []model.WindowFingerprint
}

func NewDeduper(timeToleranceMs int64) *Deduper {
	if timeToleranceMs < 0 {
		timeToleranceMs = 0
	}
	return &Deduper{TimeToleranceMs: timeToleranceMs, MinWindows: 1}
}

// NewDeduperFromState reconstructs cross-segment state after a checkpoint.
func NewDeduperFromState(
	segments []model.Segment,
	fingerprints []model.WindowFingerprint,
	mappings []model.DedupeMapping,
	timeToleranceMs int64,
) *Deduper {
	d := NewDeduper(timeToleranceMs)
	d.Mappings = append([]model.DedupeMapping(nil), mappings...)
	d.Duplicate = len(mappings)
	bySegment := make(map[int][]model.WindowFingerprint)
	for _, fp := range fingerprints {
		bySegment[fp.SegmentIndex] = append(bySegment[fp.SegmentIndex], fp)
	}
	for _, seg := range segments {
		d.seen = append(d.seen, seenSegment{
			Segment:      seg,
			Fingerprints: append([]model.WindowFingerprint(nil), bySegment[seg.Index]...),
		})
	}
	return d
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Add evaluates a segment. On a duplicate it returns the source mapping and
// marks the supplied segment's MergedInto field (a copy is returned).
func (d *Deduper) Add(seg model.Segment, fps []model.WindowFingerprint) (model.Segment, *model.DedupeMapping) {
	for _, prior := range d.seen {
		if mapping, ok := d.match(prior, seg, fps); ok {
			kept := prior.Segment.Index
			seg.MergedInto = &kept
			d.Duplicate++
			d.Mappings = append(d.Mappings, mapping)
			return seg, &mapping
		}
	}
	d.seen = append(d.seen, seenSegment{Segment: seg, Fingerprints: append([]model.WindowFingerprint(nil), fps...)})
	return seg, nil
}

func (d *Deduper) match(prior seenSegment, seg model.Segment, fps []model.WindowFingerprint) (model.DedupeMapping, bool) {
	a := prior.Fingerprints
	b := fps
	if len(a) < d.MinWindows || len(a) != len(b) {
		return model.DedupeMapping{}, false
	}
	durationA := prior.Segment.EndMs - prior.Segment.StartMs
	durationB := seg.EndMs - seg.StartMs
	if abs64(durationA-durationB) > d.TimeToleranceMs {
		return model.DedupeMapping{}, false
	}

	matching := 0
	for i := range a {
		if !a[i].Complete || !b[i].Complete || a[i].Hash != b[i].Hash {
			continue
		}
		relStartA := a[i].StartMs - prior.Segment.StartMs
		relStartB := b[i].StartMs - seg.StartMs
		relEndA := a[i].EndMs - prior.Segment.StartMs
		relEndB := b[i].EndMs - seg.StartMs
		if abs64(relStartA-relStartB) <= d.TimeToleranceMs && abs64(relEndA-relEndB) <= d.TimeToleranceMs {
			matching++
		}
	}
	// Exact generated repeats have every complete window equal. Require all
	// windows so coincidental silence windows do not merge whole segments.
	if matching != len(a) {
		return model.DedupeMapping{}, false
	}
	m := model.DedupeMapping{
		KeptSegment:     prior.Segment.Index,
		MergedSegment:   seg.Index,
		KeptStartMs:     prior.Segment.StartMs,
		MergedStartMs:   seg.StartMs,
		StartDeltaMs:    seg.StartMs - prior.Segment.StartMs,
		DurationDeltaMs: durationB - durationA,
		MatchingWindows: matching,
		TotalWindows:    len(a),
		TimeToleranceMs: d.TimeToleranceMs,
	}
	return m, true
}

// MappingsSnapshot returns a copy safe for checkpoint/report serialization.
func (d *Deduper) MappingsSnapshot() []model.DedupeMapping {
	return append([]model.DedupeMapping(nil), d.Mappings...)
}
