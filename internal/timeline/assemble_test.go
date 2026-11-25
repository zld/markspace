package timeline

import (
	"testing"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
)

func TestAssembleRejectsOutOfBoundsSegmentWithoutClamping(t *testing.T) {
	man := &mp4.Manifest{
		TimelineScale: 1000,
		Tracks: []mp4.Track{{
			TrackID: 1, Kind: "video", Timescale: 1000, Duration: 300,
		}},
		Samples: []mp4.Sample{{
			TrackID: 1, Index: 0, PTS: 0, DTS: 0, Duration: 100, Size: 1,
		}},
		Segments: []mp4.Segment{{
			ID: "seg-out", TrackID: 1, TimelineStart: 0, TimelineEnd: 400,
			FirstSampleIndex: 0, LastSampleIndex: 0,
		}},
	}
	rep := Assemble(man, nil)
	if !hasCode(rep.Issues, fault.CodeEditOutOfRange) {
		t.Fatalf("issues=%v, want edit_out_of_range", rep.Issues)
	}
	if len(rep.Segments) != 1 || rep.Segments[0].TimelineEnd != 400 {
		t.Fatalf("out-of-range time was changed/clamped: %+v", rep.Segments)
	}
}

func TestAssembleRejectsUncoveredOverlap(t *testing.T) {
	man := &mp4.Manifest{
		TimelineScale: 1000,
		Tracks: []mp4.Track{{
			TrackID: 1, Kind: "video", Timescale: 1000, Duration: 1000,
		}},
		Samples: []mp4.Sample{
			{TrackID: 1, Index: 0, PTS: 0, Duration: 100},
			{TrackID: 1, Index: 1, PTS: 100, Duration: 100},
		},
		Segments: []mp4.Segment{
			{ID: "a", TrackID: 1, TimelineStart: 0, TimelineEnd: 300, FirstSampleIndex: 0, LastSampleIndex: 0},
			{ID: "b", TrackID: 1, TimelineStart: 250, TimelineEnd: 550, FirstSampleIndex: 1, LastSampleIndex: 1},
		},
	}
	rep := Assemble(man, nil)
	if !hasCode(rep.Issues, fault.CodeInvalidOverlap) {
		t.Fatalf("issues=%v, want invalid_overlap", rep.Issues)
	}
}

func hasCode(issues []*fault.Error, code fault.Code) bool {
	for _, issue := range issues {
		if issue != nil && issue.Code == code {
			return true
		}
	}
	return false
}
