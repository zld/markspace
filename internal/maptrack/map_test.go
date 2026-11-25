package maptrack

import (
	"testing"

	"mediacore/internal/model"
)

func testTracks() []model.Track {
	return []model.Track{
		{ID: "mp4:1", Kind: model.TrackVideo, Timescale: 90000},
		{ID: "mp4:2", Kind: model.TrackSubtitle, Timescale: 1000},
	}
}

func testOuts() []model.OutputTrack {
	return []model.OutputTrack{
		{ID: "out:v", Kind: model.TrackVideo, Timescale: 90000, Accepts: []model.EventKind{model.EventMedia, model.EventSEI}},
		{ID: "out:s", Kind: model.TrackSubtitle, Timescale: 1000, Accepts: []model.EventKind{model.EventSubtitle}},
	}
}

func TestApplySortsAndAssigns(t *testing.T) {
	events := []model.Event{
		{TrackID: "mp4:2", Kind: model.EventSubtitle, Ticks: 5000, SrcOffset: 300},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 9000, SrcOffset: 100},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 0, SrcOffset: 50},
		{TrackID: "mp4:2", Kind: model.EventSubtitle, Ticks: 5000, SrcOffset: 200},
	}
	rules := []Rule{{In: "mp4:1", Out: "out:v"}, {In: "mp4:2", Out: "out:s"}}
	got, err := Apply(events, testTracks(), rules, testOuts())
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []int64{50, 200, 300, 100}
	for i, w := range wantOrder {
		if got[i].SrcOffset != w {
			t.Fatalf("position %d: srcOffset=%d, want %d", i, got[i].SrcOffset, w)
		}
	}
	if got[0].OutTrackID != "out:v" || got[2].OutTrackID != "out:s" {
		t.Fatalf("bad out assignment: %+v", got)
	}
	// Determinism: same input twice, identical result.
	again, _ := Apply(events, testTracks(), rules, testOuts())
	for i := range got {
		if got[i].Ticks != again[i].Ticks || got[i].SrcOffset != again[i].SrcOffset ||
			got[i].OutTrackID != again[i].OutTrackID || got[i].Seq != again[i].Seq {
			t.Fatalf("non-deterministic at %d", i)
		}
	}
}

func TestApplyErrors(t *testing.T) {
	events := []model.Event{{TrackID: "mp4:1", Kind: model.EventMedia}}
	// Missing target.
	if _, err := Apply(events, testTracks(), []Rule{{In: "mp4:1", Out: "nope"}, {In: "mp4:2", Out: "out:s"}}, testOuts()); err == nil {
		t.Fatal("want missing-target error")
	}
	// Duplicate mapping.
	if _, err := Apply(events, testTracks(), []Rule{{In: "mp4:1", Out: "out:v"}, {In: "mp4:1", Out: "out:v"}}, testOuts()); err == nil {
		t.Fatal("want duplicate-mapping error")
	}
	// Unmapped input track.
	if _, err := Apply(events, testTracks(), []Rule{{In: "mp4:1", Out: "out:v"}}, testOuts()); err == nil {
		t.Fatal("want unmapped-track error")
	}
	// Incompatible kind.
	bad := []model.Event{{TrackID: "mp4:2", Kind: model.EventMedia}}
	if _, err := Apply(bad, testTracks(), []Rule{{In: "mp4:1", Out: "out:v"}, {In: "mp4:2", Out: "out:s"}}, testOuts()); err == nil {
		t.Fatal("want incompatible-kind error")
	}
}

func TestCalibrateTracks(t *testing.T) {
	events := []model.Event{
		{TrackID: "mp4:1", Ticks: 0},
		{TrackID: "mp4:2", Ticks: 4500},
		{TrackID: "mp4:2", Ticks: 9000},
	}
	reps, err := CalibrateTracks(events, testTracks(), 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 || reps[1].Offset != -4500 {
		t.Fatalf("unexpected reports: %+v", reps)
	}
	if events[1].Ticks != 0 || events[2].Ticks != 4500 {
		t.Fatalf("events not shifted: %+v", events)
	}
	// Over threshold fails and keeps position info.
	big := []model.Event{{TrackID: "mp4:1", Ticks: 0}, {TrackID: "mp4:2", Ticks: 999999}}
	if _, err := CalibrateTracks(big, testTracks(), 1000); err == nil {
		t.Fatal("want drift error")
	} else if me := err.(*model.Error); me.Category != model.ErrDrift || me.TrackID != "mp4:2" {
		t.Fatalf("unexpected error: %v", err)
	}
}
