package remux_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/demux"
	"mediacore/internal/model"
	"mediacore/internal/remux"
)

func samplePayload(b ...byte) []byte { return b }

func TestWriteThenDemuxRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.mp4")
	outs := []model.OutputTrack{
		{ID: "out:v", Kind: model.TrackVideo, Timescale: 90000, Accepts: []model.EventKind{model.EventMedia, model.EventSEI}},
		{ID: "out:s", Kind: model.TrackSubtitle, Timescale: 1000, Accepts: []model.EventKind{model.EventSubtitle}},
		{ID: "out:m", Kind: model.TrackMeta, Timescale: 90000, Accepts: []model.EventKind{model.EventID3, model.EventEMSG, model.EventChapter}},
	}
	// VFR video: irregular durations.
	events := []model.Event{
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 0, Duration: 3000, SrcOffset: 100, Size: 4, OutTrackID: "out:v", Payload: samplePayload(0, 0, 0, 1)},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 3000, Duration: 4500, SrcOffset: 104, Size: 3, OutTrackID: "out:v", Payload: samplePayload(9, 8, 7)},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 7500, Duration: 1500, SrcOffset: 107, Size: 5, OutTrackID: "out:v", Payload: samplePayload(1, 2, 3, 4, 5)},
		{TrackID: "mp4:2", Kind: model.EventSubtitle, Ticks: 1000, Duration: 2000, SrcOffset: 200, Size: 5, OutTrackID: "out:s", Payload: []byte("hello")},
		{TrackID: "mp4:3", Kind: model.EventID3, Ticks: 2000, SrcOffset: 300, Size: 4, OutTrackID: "out:m", Payload: []byte("ID3x")},
		{TrackID: "mp4:emsg", Kind: model.EventEMSG, Ticks: 4000, SrcOffset: 400, Size: 3, OutTrackID: "out:m", Payload: []byte("msg")},
		{TrackID: "mp4:chpl", Kind: model.EventChapter, Ticks: 0, SrcOffset: 500, Size: 3, OutTrackID: "out:m", Payload: []byte("ch1")},
	}
	if err := remux.WriteMP4(path, outs, events); err != nil {
		t.Fatal(err)
	}
	tracks, got, err := demux.DemuxMP4(path, demux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 5 { // 3 output tracks + emsg/chpl pseudo tracks
		t.Fatalf("tracks = %d, want 5", len(tracks))
	}
	// Media samples: same count, same ticks, identical payload bytes.
	var gotMedia []model.Event
	for _, e := range got {
		if e.Kind == model.EventMedia {
			gotMedia = append(gotMedia, e)
		}
	}
	if len(gotMedia) != 3 {
		t.Fatalf("media events = %d, want 3", len(gotMedia))
	}
	for i, src := range events[:3] {
		if gotMedia[i].Ticks != src.Ticks {
			t.Errorf("sample %d ticks = %d, want %d", i, gotMedia[i].Ticks, src.Ticks)
		}
		if !bytes.Equal(gotMedia[i].Payload, src.Payload) {
			t.Errorf("sample %d payload changed: %x != %x", i, gotMedia[i].Payload, src.Payload)
		}
	}
	// Subtitle, ID3, emsg, chapter survive.
	kinds := map[model.EventKind]int{}
	for _, e := range got {
		kinds[e.Kind]++
	}
	if kinds[model.EventSubtitle] != 1 || kinds[model.EventID3] != 1 ||
		kinds[model.EventEMSG] != 1 || kinds[model.EventChapter] != 1 {
		t.Fatalf("timed metadata lost: %v", kinds)
	}
	for _, e := range got {
		if e.Kind == model.EventChapter && string(e.Payload) != "ch1" {
			t.Errorf("chapter title = %q", e.Payload)
		}
		if e.Kind == model.EventSubtitle && string(e.Payload) != "hello" {
			t.Errorf("subtitle = %q", e.Payload)
		}
	}
}

func TestWriteRejectsUnknownOutput(t *testing.T) {
	dir := t.TempDir()
	err := remux.WriteMP4(filepath.Join(dir, "x.mp4"), nil, []model.Event{
		{Kind: model.EventMedia, OutTrackID: "ghost"},
	})
	if err == nil {
		t.Fatal("want error for unknown output track")
	}
	me, ok := err.(*model.Error)
	if !ok || me.Stage != model.StageRemux {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnifiedTimescaleConstant(t *testing.T) {
	if clock.UnifiedTimescale != 90000 {
		t.Fatalf("unified timescale changed: %d", clock.UnifiedTimescale)
	}
}
