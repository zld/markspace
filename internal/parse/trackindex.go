package parse

import "mediaforensics/internal/model"

// TrackIndex is the parsed sample table of one track, with provenance for
// every geometric claim.
type TrackIndex struct {
	TrackID        int
	Kind           string
	Timescale      int64
	Delta          int64
	EarliestOffset int64
	SampleCount    int
	SampleSize     []int64
	SampleOffset   []int64 // absolute file offsets; -1 when unknown
	Keyframes      []bool
	Intervals      []model.Interval
	Errors         []*model.ForensicError
	Rebuilt        bool
}
