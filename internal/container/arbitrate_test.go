package container

import (
	"testing"

	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
)

func parsePair(t *testing.T, seed int64, mxfDropIndex bool) (*MXFMeta, *MP4Meta) {
	t.Helper()
	mxfSpec := fixture.DefaultMXFSpec(seed)
	mxfSpec.DropIndex = mxfDropIndex
	mxfData, _ := fixture.GenerateMXF(mxfSpec)
	mxf, err := ParseMXF(mxfData)
	if err != nil {
		t.Fatalf("mxf parse: %v", err)
	}
	mp4Data, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(seed))
	mp4, err := ParseMP4(mp4Data)
	if err != nil {
		t.Fatalf("mp4 parse: %v", err)
	}
	return mxf, mp4
}

func TestArbitrateMXFWinsColorConflict(t *testing.T) {
	mxf, mp4 := parsePair(t, 7, false)
	var book model.ErrorBook
	out := Arbitrate(mxf, mp4, &book)

	// MXF primaries=6, MP4 primaries=1 -> MXF wins, conflict recorded.
	if out.Color.Primaries != 6 || out.Color.Source != "mxf" {
		t.Fatalf("winner color = %+v, want MXF primaries 6", out.Color)
	}
	if out.ConflictCount != 1 {
		t.Fatalf("conflicts = %d, want 1", out.ConflictCount)
	}
	if out.Conflicts[0].Field != "color_primaries" || out.Conflicts[0].Winner != "mxf" {
		t.Fatalf("conflict = %+v", out.Conflicts[0])
	}
	if !book.Has(model.CodeConflict) {
		t.Fatal("conflict must appear in the error book")
	}
	// Rotation agrees (both 0), so no second conflict.
	if out.Rotation != 0 {
		t.Fatalf("rotation = %d, want 0", out.Rotation)
	}
}

func TestArbitrateRotationConflict(t *testing.T) {
	mxf, mp4 := parsePair(t, 7, false)
	mp4.FileMeta.Rotation = 270
	var book model.ErrorBook
	out := Arbitrate(mxf, mp4, &book)
	if out.Rotation != 0 {
		t.Fatalf("rotation winner = %d, want MXF value 0", out.Rotation)
	}
	found := false
	for _, c := range out.Conflicts {
		if c.Field == "rotation" && c.Winner == "mxf" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rotation conflict not recorded: %+v", out.Conflicts)
	}
}

func TestArbitrateDefaultsWhenMXFAbsent(t *testing.T) {
	_, mp4 := parsePair(t, 7, false)
	var book model.ErrorBook
	out := Arbitrate(nil, mp4, &book)
	if out.Color.Source != "mp4" || out.Color.Primaries != 1 {
		t.Fatalf("MP4 should win with no MXF: %+v", out.Color)
	}
	out2 := Arbitrate(nil, nil, &book)
	if out2.Color.Source != "default" {
		t.Fatalf("default color missing: %+v", out2.Color)
	}
	if out2.Rotation != 0 {
		t.Fatalf("default rotation = %d", out2.Rotation)
	}
}

func TestArbitrateTrackUnion(t *testing.T) {
	mxf, mp4 := parsePair(t, 7, false)
	var book model.ErrorBook
	out := Arbitrate(mxf, mp4, &book)
	// 1 MXF video + 2 MP4 tracks.
	if len(out.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(out.Tracks))
	}
	if out.VideoTimeScale <= 0 {
		t.Fatal("winning video timescale missing")
	}
}
