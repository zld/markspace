package timeline

import (
	"testing"

	"mediasync/internal/ingest"
)

func TestMergeDeterministicOrderAndStats(t *testing.T) {
	opus := &ingest.OpusResult{
		TrackID: "opus",
		Packets: []ingest.OpusPacket{
			{Ordinal: 0, ExtSequence: 1000, StartTicks: 100},
			{Ordinal: 1, ExtSequence: 1001, StartTicks: 200},
		},
		Stats: ingest.RTPStats{Unique: 2, Duplicates: 1, Gaps: 1, GapSequences: []int64{1002}},
	}
	aac := &ingest.AACResult{TrackID: "aac", Samples: []ingest.AACSample{
		{Index: 0, StartTicks: 150, DurationTicks: 50},
	}}
	ref := int64(1000)
	subs := &ingest.SubtitleFile{Cues: []ingest.SubtitleCue{
		{Index: 1, StartMS: 1000, EndMS: 2000, ReferenceStartMS: &ref, Text: "hello"},
	}}
	tl, err := Merge([]Input{
		{Spec: ingest.TrackSpec{ID: "opus", Kind: ingest.KindOpus}, Opus: opus},
		{Spec: ingest.TrackSpec{ID: "aac", Kind: ingest.KindAAC}, AAC: aac},
		{Spec: ingest.TrackSpec{ID: "subs", Kind: ingest.KindSubtitle}, Subtitles: subs},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Stats.Tracks != 3 || tl.Stats.Unique != 2 || tl.Stats.Duplicates != 1 || tl.Stats.Gaps != 1 {
		t.Fatalf("bad stats: %+v", tl.Stats)
	}
	if tl.Stats.LossPercent != "33.33%" {
		t.Fatalf("loss=%s", tl.Stats.LossPercent)
	}
	if tl.Quality.OK {
		t.Fatalf("33%% loss should fail quality: %+v", tl.Quality)
	}
	if tl.Events[0].Kind != KindOpus || tl.Events[0].EndTicks != 200 {
		t.Fatalf("adjacent Opus end=%d, want next timestamp 200", tl.Events[0].EndTicks)
	}
	wantKinds := []EventKind{KindOpus, KindAAC, KindOpus, KindGap, KindSubtitle}
	if len(tl.Events) != len(wantKinds) {
		t.Fatalf("events=%d want %d", len(tl.Events), len(wantKinds))
	}
	for i, want := range wantKinds {
		if tl.Events[i].Kind != want {
			t.Fatalf("event %d kind=%s want %s (%+v)", i, tl.Events[i].Kind, want, tl.Events[i])
		}
		if tl.Events[i].Index != i {
			t.Fatalf("event index %d not contiguous", i)
		}
	}
}

func TestQualityThresholdBoundaries(t *testing.T) {
	// Exactly 1 gap among 9 unique + 1 gap is exactly 10%.
	tl, err := Merge([]Input{{
		Spec: ingest.TrackSpec{ID: "opus", Kind: ingest.KindOpus},
		Opus: &ingest.OpusResult{Stats: ingest.RTPStats{Unique: 9, Gaps: 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.CheckQuality(); err != nil {
		t.Fatalf("10%% loss must pass: %v", err)
	}

	bad, err := Merge([]Input{{
		Spec: ingest.TrackSpec{ID: "opus", Kind: ingest.KindOpus},
		Opus: &ingest.OpusResult{Stats: ingest.RTPStats{Unique: 8, Gaps: 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.CheckQuality(); err == nil {
		t.Fatal("1/9 loss must fail")
	}
}

func TestSubtitleDeviationGate(t *testing.T) {
	ref := int64(100)
	inputs := []Input{{
		Spec: ingest.TrackSpec{ID: "subs", Kind: ingest.KindSubtitle},
		Subtitles: &ingest.SubtitleFile{Cues: []ingest.SubtitleCue{
			{Index: 1, StartMS: 102, EndMS: 200, ReferenceStartMS: &ref},
		}},
	}}
	tl, err := Merge(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Quality.OK {
		t.Fatal("2ms deviation must fail")
	}
	if err := tl.CheckQuality(); err == nil {
		t.Fatal("expected quality error")
	}
}
