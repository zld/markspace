package pipeline

import "mediadist/internal/merr"

// newTestError builds an error with every span field populated so the format
// test can assert each one is rendered.
func newTestError() *merr.Error {
	return merr.New(merr.StageDemux, "sample_missing_packets",
		merr.Span{Stream: 3, Seq: 17, Offset: 42, Length: 8, TrackID: 7}, true,
		"missing %d packets", 2)
}
