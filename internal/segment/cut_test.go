package segment_test

import (
	"errors"
	"testing"

	"github.com/zld/mediacore/internal/ingest"
	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/samplegen"
	"github.com/zld/mediacore/internal/segment"
	"github.com/zld/mediacore/internal/trace"
)

func standardParams() segment.Params {
	return segment.Params{
		Cuts: []segment.Cut{{At: 400_000_000, Label: "xfade-start"}, {At: 600_000_000, Label: "xfade-end"}},
		Transitions: []segment.Transition{{
			ID: "xfade-1", Start: 400_000_000, End: 600_000_000, FromTrack: 1, ToTrack: 2,
		}},
	}
}

func normalizedFixture(t *testing.T, data []byte) *model.Timeline {
	t.Helper()
	doc, err := ingest.ParseMP4(data)
	if err != nil && doc == nil {
		t.Fatal(err)
	}
	tl, err := model.Normalize("digest", doc.Tracks, doc.Damages)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func sourceTracks(seg segment.Segment) []int {
	var ids []int
	for _, s := range seg.Sources {
		ids = append(ids, s.TrackID)
	}
	return ids
}

func TestBuildNormalTransitionMappings(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	tl := normalizedFixture(t, g.Data)
	segs, err := segment.Build(tl, standardParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 {
		t.Fatalf("segment count = %d: %+v", len(segs), segs)
	}
	if segs[0].Interval != (model.Interval{Start: 0, End: 400_000_000}) {
		t.Fatalf("seg0 = %+v", segs[0].Interval)
	}
	if got := sourceTracks(segs[0]); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("seg0 tracks = %v", got)
	}
	mid := segs[1]
	if !mid.Transition || mid.TransitionID != "xfade-1" {
		t.Fatalf("middle segment transition = %+v", mid)
	}
	if got := sourceTracks(mid); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("transition tracks = %v", got)
	}
	var has1, has2 bool
	for _, src := range mid.Sources {
		if src.TrackID == 1 {
			has1 = src.Timeline == (model.Interval{Start: 400_000_000, End: 600_000_000})
		}
		if src.TrackID == 2 {
			has2 = src.Timeline == (model.Interval{Start: 400_000_000, End: 600_000_000}) &&
				src.Local == (model.Interval{Start: 0, End: 200_000_000})
		}
	}
	if !has1 || !has2 {
		t.Fatalf("overlap source mappings incomplete: %+v", mid.Sources)
	}
	if segs[2].End != model.NS(1_000_000_000) {
		t.Fatalf("final end = %d", segs[2].End)
	}
}

func TestCutAtDamagedBoundaryReturnsPriorSegmentsAndLocation(t *testing.T) {
	g, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	cutOffset, ok := samplegen.TruncateInSample(g, 1, 5, 16)
	if !ok {
		t.Fatal("inject truncation")
	}
	doc, parseErr := ingest.ParseMP4(g.Data)
	if !trace.HasCode(parseErr, trace.CodeTruncated) {
		t.Fatalf("parse error = %v", parseErr)
	}
	tl, err := model.Normalize("digest", doc.Tracks, doc.Damages)
	if err != nil {
		t.Fatal(err)
	}
	params := standardParams()
	params.Cuts = append(params.Cuts, segment.Cut{At: 500_000_000, Label: "inside-transition-boundary"})
	segs, err := segment.Build(tl, params)
	if len(segs) != 2 {
		t.Fatalf("expected complete segments before damaged cut, got %d", len(segs))
	}
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodeCutPointCorrupt || te.Stage != trace.StageCut {
		t.Fatalf("error = %v", err)
	}
	if !te.HasTime || te.TimeNS != 500_000_000 || !te.HasTrack || te.TrackID != 1 {
		t.Fatalf("location = %+v", te)
	}
	if !te.HasByteOffset || te.ByteOffset != cutOffset {
		t.Fatalf("offset = %d want %d", te.ByteOffset, cutOffset)
	}
	if !trace.HasCode(err, trace.CodeTruncated) {
		t.Fatalf("underlying truncation not propagated: %v", err)
	}
}
