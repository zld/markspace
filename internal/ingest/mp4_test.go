package ingest

import (
	"testing"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/samplegen"
)

func TestParseGeneratedMP4(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseMP4(g.Data)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if !doc.IndexPresent {
		t.Fatal("stts index should be present")
	}
	if len(doc.Tracks) != 3 {
		t.Fatalf("track count = %d", len(doc.Tracks))
	}
	tl, err := model.Normalize("digest", doc.Tracks, doc.Damages)
	if err != nil {
		t.Fatal(err)
	}
	tr2 := tl.Track(2)
	if tr2 == nil || tr2.OffsetNS != 400_000_000 {
		t.Fatalf("track2 offset = %+v", tr2)
	}
	if len(tl.Track(1).Samples) != 6 || len(tr2.Samples) != 6 || len(tl.Track(3).Samples) != 10 {
		t.Fatalf("sample counts = %d,%d,%d", len(tl.Track(1).Samples), len(tr2.Samples), len(tl.Track(3).Samples))
	}
	if tr2.Start != 400_000_000 || tr2.End != 1_000_000_000 {
		t.Fatalf("track2 range = [%d,%d)", tr2.Start, tr2.End)
	}
}

func TestParseMissingIndexRebuildsFromBoxes(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(false))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseMP4(g.Data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.IndexPresent {
		t.Fatal("index should be absent")
	}
	if len(doc.TrackByID(1).Samples) != 6 {
		t.Fatalf("rebuilt samples = %d", len(doc.TrackByID(1).Samples))
	}
}
