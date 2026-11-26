package pipeline

import (
	"mediad/internal/model"
	"mediad/internal/scan"
	"mediad/internal/timeline"
)

// normalizedForTest builds a small in-memory timeline with two tracks for
// pure-logic tests (no ffmpeg, no filesystem).
func normalizedForTest() *timeline.Normalized {
	base := model.Rational{Num: 1, Den: 1000}
	return &timeline.Normalized{
		SourceHash: "h",
		Tracks: []timeline.NormalizedTrack{
			{
				TrackSummary: scan.TrackSummary{ID: 1, Kind: scan.KindVideo, TimeBase: base},
				Events: []timeline.NormalizedEvent{
					{TimedEvent: scan.TimedEvent{ID: "e", Tick: 500, TimeBase: base},
						StartNanos: 500_000_000, EndNanos: 600_000_000},
				},
			},
			{
				TrackSummary: scan.TrackSummary{ID: 2, Kind: scan.KindSubtitle, TimeBase: base},
			},
		},
	}
}
