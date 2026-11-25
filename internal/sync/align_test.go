package sync

import (
	"strconv"
	"testing"

	"mediasync/internal/model"
)

func TestAlignProjectsOntoMasterTimeline(t *testing.T) {
	rec := model.NewRecorder()
	cues := []Cue{
		{Track: "video", Index: 0, Rate: 30, Start: 0, Duration: 1},       // 0..3000 ticks
		{Track: "video", Index: 1, Rate: 30, Start: 1, Duration: 1},       // 3000..6000
		{Track: "audio", Index: 0, Rate: 48000, Start: 0, Duration: 48000}, // 0..90000
		{Track: "subtitle", Index: 0, Rate: 1000, Start: 0, Duration: 500}, // 0..45000
	}
	res, err := Align(cues, DefaultAlignmentConfig(), rec)
	if err != nil || rec.HasError() {
		t.Fatalf("align failed: %v %v", err, rec.Records())
	}
	if len(res.Cues) != 4 {
		t.Fatalf("aligned cues = %d, want 4", len(res.Cues))
	}
	// Sorted by (start, track, index): the two start-0 cues order audio then
	// subtitle alphabetically; video frame 1 lands at 3000..6000 master ticks.
	v0 := res.Cues[2]
	v1 := res.Cues[3]
	if v0.Track != "video" || v0.StartTick != 0 || v0.EndTick != 3000 {
		t.Fatalf("video frame 0 wrong: %+v", v0)
	}
	if v1.Track != "video" || v1.StartTick != 3000 || v1.EndTick != 6000 {
		t.Fatalf("video frame 1 wrong: %+v", v1)
	}
	// Tracks are de-duplicated and sorted.
	wantTracks := []string{"audio", "subtitle", "video"}
	if len(res.Tracks) != 3 {
		t.Fatalf("tracks = %v, want %v", res.Tracks, wantTracks)
	}
	for i := range wantTracks {
		if res.Tracks[i] != wantTracks[i] {
			t.Fatalf("tracks = %v, want %v", res.Tracks, wantTracks)
		}
	}
}

func TestAlignMeasuresDrift(t *testing.T) {
	rec := model.NewRecorder()
	// Video at 30 fps; audio starts 30 ms late (one frame) and stays there.
	cues := []Cue{
		{Track: "video", Index: 0, Rate: 30, Start: 0, Duration: 1},
		{Track: "video", Index: 1, Rate: 30, Start: 1, Duration: 1},
		{Track: "audio", Index: 0, Rate: 1000, Start: 30, Duration: 30},
		{Track: "audio", Index: 1, Rate: 1000, Start: 60, Duration: 30},
	}
	// Audio cue 0 (2700 ticks) is nearest video frame 1 (3000): drift 300.
	// Audio cue 1 (5400 ticks) is nearest frame 1 (3000): drift 2400 ticks,
	// i.e. 26.7 ms rounded to 26 ms by the integer master scale.
	res, err := Align(cues, AlignmentConfig{MaxDriftMillis: 100}, rec)
	if err != nil {
		t.Fatalf("within-limit drift must pass: %v", err)
	}
	if res.MaxDriftTick != 2400 || res.MaxDriftMs != 26 {
		t.Fatalf("max drift = %d ticks / %d ms, want 2400 / 26",
			res.MaxDriftTick, res.MaxDriftMs)
	}
	if len(res.Drifts) != 2 {
		t.Fatalf("drift samples = %d, want 2", len(res.Drifts))
	}
}

func TestAlignFailsBeyondThreshold(t *testing.T) {
	rec := model.NewRecorder()
	cues := []Cue{
		{Track: "video", Index: 0, Rate: 30, Start: 0, Duration: 1},
		{Track: "audio", Index: 0, Rate: 1000, Start: 80, Duration: 20}, // 80 ms late
	}
	res, err := Align(cues, AlignmentConfig{MaxDriftMillis: 40}, rec)
	if err == nil {
		t.Fatal("drift beyond threshold must fail the stage")
	}
	var found bool
	for _, r := range rec.Records() {
		if r.Code == model.ErrAlignmentExceeded && r.EndTick == res.MaxDriftTick {
			found = true
		}
	}
	if !found {
		t.Fatalf("want located ALIGNMENT_EXCEEDED, got %v", rec.Records())
	}
	if res.MaxDriftMs != 80 {
		t.Fatalf("max drift ms = %d, want 80", res.MaxDriftMs)
	}
}

func TestAlignRejectsIllegalRate(t *testing.T) {
	rec := model.NewRecorder()
	cues := []Cue{
		{Track: "audio", Index: 0, Rate: 0, Start: 10, Duration: 10},
	}
	if _, err := Align(cues, DefaultAlignmentConfig(), rec); err == nil {
		t.Fatal("zero rate must fail")
	}
	var found bool
	for _, r := range rec.Records() {
		if r.Code == model.ErrInvalidTimestamp && r.Track == "audio" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want located INVALID_TIMESTAMP, got %v", rec.Records())
	}
}

func TestAlignDeterministicOrdering(t *testing.T) {
	rec := model.NewRecorder()
	// Same start time across tracks; ordering must be stable by track then index.
	cues := []Cue{
		{Track: "video", Index: 1, Rate: 1000, Start: 100, Duration: 10},
		{Track: "audio", Index: 0, Rate: 1000, Start: 100, Duration: 10},
		{Track: "video", Index: 0, Rate: 1000, Start: 100, Duration: 10},
	}
	res, err := Align(cues, DefaultAlignmentConfig(), rec)
	if err != nil {
		t.Fatal(err)
	}
	got := [][2]string{}
	for _, c := range res.Cues {
		got = append(got, [2]string{c.Track, strconv.Itoa(c.Index)})
	}
	want := [][2]string{{"audio", "0"}, {"video", "0"}, {"video", "1"}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestAlignMissingTrackSkipsDrift(t *testing.T) {
	rec := model.NewRecorder()
	// Subtitle-only timeline: drift is undefined, must not error.
	cues := []Cue{{Track: "subtitle", Index: 0, Rate: 1000, Start: 0, Duration: 100}}
	res, err := Align(cues, DefaultAlignmentConfig(), rec)
	if err != nil || rec.HasError() {
		t.Fatalf("subtitle-only align must pass: %v %v", err, rec.Records())
	}
	if res.MaxDriftTick != 0 || len(res.Drifts) != 0 {
		t.Fatalf("drift must be zero/absent without audio+video: %+v", res)
	}
}
