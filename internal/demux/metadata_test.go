package demux

import (
	"testing"

	"mediarc/internal/fixture"
	"mediarc/internal/model"
)

func TestAllMetadataBuildersParse(t *testing.T) {
	emsg, err := fixture.Emsg(12, 9, "x")
	if err != nil {
		t.Fatal(err)
	}
	cases := []model.TimedEvent{
		{Kind: model.MetaSubtitle, TrackID: 3, Payload: fixture.WebVTT(10, "hello")},
		{Kind: model.MetaChapter, TrackID: 4, Payload: fixture.ChapterBook([]fixture.Chapter{{PTSMS: 0, Title: "A"}, {PTSMS: 10, Title: "B"}})},
		{Kind: model.MetaID3, TrackID: 5, Payload: fixture.ID3Tag("Title", "Artist")},
		{Kind: model.MetaEmsg, TrackID: 5, Payload: emsg},
		{Kind: model.MetaSEI, TrackID: 1, Payload: fixture.SEIUserdata("sei")},
	}
	for i := range cases {
		if err := ParseEvent(&cases[i]); err != nil {
			t.Fatalf("%s parse failed: %v", cases[i].Kind, err)
		}
		if cases[i].Text == "" && cases[i].Kind != model.MetaEmsg {
			t.Fatalf("%s produced no text", cases[i].Kind)
		}
	}
}

func TestTruncatedID3ReportsTrackAndOffset(t *testing.T) {
	full := fixture.ID3Tag("Title", "Artist")
	ev := model.TimedEvent{Kind: model.MetaID3, TrackID: 8, Payload: full[:len(full)-4], SourceOffset: 1234}
	err := ParseEvent(&ev)
	if !model.HasCode(err, model.CodeTruncated) {
		t.Fatalf("want truncation, got %v", err)
	}
	me, _ := model.AsMediaError(err)
	if me.TrackID == nil || *me.TrackID != 8 || me.SourceOffset == nil || *me.SourceOffset != 1234 {
		t.Fatalf("missing track/offset context: %+v", me)
	}
}

func TestUnsupportedMetadataKind(t *testing.T) {
	err := ParseEvent(&model.TimedEvent{Kind: "unknown", TrackID: 1})
	if !model.HasCode(err, model.CodeUnsupported) {
		t.Fatalf("want unsupported, got %v", err)
	}
}
