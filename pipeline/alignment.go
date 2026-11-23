package pipeline

import "mediamux/media"

// CompareManifestToMedia cross-checks declared segment durations against the
// durations actually present in the analysed fragments. A declared length
// that disagrees with the media beyond tolerance is reported as an alignment
// error at the manifest declaration's byte offset, so "the playlist says one
// thing but the bytes say another" is traceable to a line.
//
// durationsMS is parallel to manifest.Segments; a zero entry means the media
// duration could not be determined and is skipped rather than guessed.
func CompareManifestToMedia(doc []SegmentRef, durationsMS []int64, toleranceMS int64) *AlignmentReport {
	rep := &AlignmentReport{ToleranceMS: toleranceMS}
	if toleranceMS <= 0 {
		rep.ToleranceMS = 2
	}
	for i := range doc {
		if i >= len(durationsMS) || durationsMS[i] <= 0 {
			continue
		}
		declared := doc[i].Duration
		declaredMS := declared.Value * 1000 / int64(declared.Timescale)
		actual := durationsMS[i]
		delta := actual - declaredMS
		if delta < 0 {
			delta = -delta
		}
		rep.Observed++
		if delta > rep.MaxDeltaMS {
			rep.MaxDeltaMS = delta
		}
		if delta > rep.ToleranceMS {
			rep.Mismatches = append(rep.Mismatches, AlignmentMismatch{
				Segment: i, SegmentID: doc[i].ID, DeclaredMS: declaredMS,
				ActualMS: actual, DeltaMS: delta, ManifestOffset: doc[i].DeclaredOffset,
			})
			rep.TotalDeltaMS += delta
		}
	}
	return rep
}

// SegmentRef is the manifest view CompareManifestToMedia needs.
type SegmentRef struct {
	ID             string
	Duration       media.Time
	DeclaredOffset int64
}

// AlignmentMismatch is one segment whose media duration contradicts the
// manifest.
type AlignmentMismatch struct {
	Segment        int    `json:"segment"`
	SegmentID      string `json:"segment_id"`
	DeclaredMS     int64  `json:"declared_ms"`
	ActualMS       int64  `json:"actual_ms"`
	DeltaMS        int64  `json:"delta_ms"`
	ManifestOffset int64  `json:"manifest_offset"`
}

// AlignmentReport summarises the manifest-versus-media comparison.
type AlignmentReport struct {
	ToleranceMS  int64               `json:"tolerance_ms"`
	Observed     int                 `json:"observed"`
	MaxDeltaMS   int64               `json:"max_delta_ms"`
	TotalDeltaMS int64               `json:"total_delta_ms"`
	Mismatches   []AlignmentMismatch `json:"mismatches,omitempty"`
}

// Aligned reports whether every observed segment agreed within tolerance.
func (a *AlignmentReport) Aligned() bool { return len(a.Mismatches) == 0 }
