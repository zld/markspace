package metadata

import (
	"testing"

	"media-audit/internal/media"
)

func TestMXFPrecedenceAndConflictParties(t *testing.T) {
	mxfColor := &media.Color{Primaries: 9, Transfer: 9, Matrix: 9}
	mp4Color := &media.Color{Primaries: 1, Transfer: 1, Matrix: 1}
	res := Resolve([]Candidate{
		{Source: string(media.FormatMP4), TrackID: 2, Meta: media.Metadata{Color: mp4Color, MetadataVersion: 7}},
		{Source: string(media.FormatMXF), TrackID: 1, Meta: media.Metadata{Color: mxfColor, MetadataVersion: 8}},
	})
	if res.Metadata.Color.Primaries != 9 {
		t.Fatalf("expected MXF color to win, got %+v", res.Metadata.Color)
	}
	var colorConflict *Conflict
	for i := range res.Conflicts {
		if res.Conflicts[i].Field == "color" {
			colorConflict = &res.Conflicts[i]
		}
	}
	if colorConflict == nil {
		t.Fatalf("expected color conflict: %+v", res.Conflicts)
	}
	if colorConflict.Winner.Source != "MXF" {
		t.Fatalf("winner = %s", colorConflict.Winner.Source)
	}
	if len(colorConflict.Losers) == 0 || colorConflict.Losers[0].Source != "MP4" {
		t.Fatalf("missing MP4 conflict party: %+v", colorConflict.Losers)
	}
}

func TestDefaultUsedWhenNoCandidate(t *testing.T) {
	res := Resolve(nil)
	if res.Metadata.Color == nil || res.Metadata.Color.Primaries != 1 {
		t.Fatalf("expected BT.709 default, got %+v", res.Metadata.Color)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("defaults must not conflict with themselves: %+v", res.Conflicts)
	}
}
