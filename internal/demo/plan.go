// Package demo wires generation, parsing, indexing, rendering and release.
package demo

import (
	"assetpack/internal/clock"
	"assetpack/internal/timeline"
)

// Plan returns the deterministic multi-track edit for a sequence.
func Plan(sequence int) timeline.Sequence {
	cut := clock.Ticks(90000)
	if sequence == 2 {
		cut = 97500 // alternate edit point; default query still contains 24 frames
	}
	return timeline.Sequence{
		ID:       sequence,
		Duration: 180000,
		Clips: []timeline.Clip{
			{
				ID: "video-a", Track: "video", Kind: "video", Start: 0,
				SourceStart: 0, SourceEnd: cut, SourceDuration: 180000, Grid: 3750,
			},
			{
				ID: "video-b", Track: "video", Kind: "video", Start: cut,
				SourceStart: cut, SourceEnd: 180000, SourceDuration: 180000, Grid: 3750,
			},
			{
				ID: "music", Track: "audio", Kind: "audio", Start: 0,
				SourceStart: 0, SourceEnd: 180000, SourceDuration: 180000, Grid: 15,
			},
		},
		Transitions: []timeline.Transition{
			{
				ID: "cut-main", Track: "video", Kind: "crossfade",
				FromClip: "video-a", ToClip: "video-b", EditPoint: cut,
				Duration: 7500, Grid: 3750,
			},
		},
	}
}
