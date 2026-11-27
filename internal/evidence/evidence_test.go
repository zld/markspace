package evidence

import (
	"encoding/json"
	"math"
	"testing"

	"regress/internal/clock"
	"regress/internal/fixture"
	"regress/internal/model"
	"regress/internal/timeline"
)

func TestFrameHashAndPixelMean(t *testing.T) {
	frame := fixture.FramePixels(0x1234, 3)
	s := model.Sample{Seq: 3, Tick: 9000, Width: fixture.FrameW,
		Height: fixture.FrameH, Frame: frame}
	fs, err := Frame(fixture.RendHigh, s)
	if err != nil {
		t.Fatal(err)
	}
	if fs.Width != fixture.FrameW || fs.Height != fixture.FrameH {
		t.Fatalf("dims wrong: %+v", fs)
	}
	if fs.Sha256 == "" || fs.Mean < 0 || fs.Mean > 255 {
		t.Fatalf("frame stats implausible: %+v", fs)
	}
	// Same pixels must hash identically; different sequence differently.
	fs2, _ := Frame(fixture.RendHigh, s)
	other := model.Sample{Seq: 4, Width: fixture.FrameW, Height: fixture.FrameH,
		Frame: fixture.FramePixels(0x1234, 4)}
	fs3, _ := Frame(fixture.RendHigh, other)
	if fs.Sha256 != fs2.Sha256 || fs.Sha256 == fs3.Sha256 {
		t.Fatal("frame hashes not discriminating/stable")
	}
}

func TestAudioPeakDBFS(t *testing.T) {
	pcm := make([]int16, 100)
	for i := range pcm {
		pcm[i] = 16384 // half-scale square wave
	}
	s := model.Sample{Seq: 1, Tick: 4800, Audio: pcm}
	as, err := Audio(fixture.AudioID, s)
	if err != nil {
		t.Fatal(err)
	}
	if as.Peak != 16384 {
		t.Fatalf("peak = %d", as.Peak)
	}
	// Half-scale peak is -6.0206 dBFS; allow tolerance for the 4-decimal
	// printing but compute against the full float here.
	want := 20 * math.Log10(0.5)
	if diff := as.PeakDBFS - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("peak dBFS = %v, want %v", as.PeakDBFS, want)
	}
	if as.RMS != as.PeakDBFS {
		t.Fatalf("square wave rms %v != peak %v", as.RMS, as.PeakDBFS)
	}
	// Full-scale positive sample must reach 0 dBFS.
	full := model.Sample{Seq: 2, Audio: []int16{32767}}
	fullStats, _ := Audio(fixture.AudioID, full)
	if fullStats.PeakDBFS >= 0 {
		t.Fatalf("32767 must be just under 0 dBFS, got %v", fullStats.PeakDBFS)
	}
}

func TestAlignmentErrors(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindID3, Events: []model.Event{
		{Key: "k1", Track: "m", Seq: 0, ObservedTick: 1150},
		{Key: "k2", Track: "m", Seq: 1, ObservedTick: 4100},
	}}}
	plan := []model.PlanItem{
		{Key: "k1", Track: "m", Seq: 0, ExpectedTick: 1000},
		{Key: "k2", Track: "m", Seq: 1, ExpectedTick: 4000},
		{Key: "k3", Track: "m", Seq: 2, ExpectedTick: 7000}, // missing
	}
	tl := timeline.Build(mp4, nil, plan)
	errs := Alignment(tl)
	if len(errs) != 3 {
		t.Fatalf("alignment rows = %d, want 3", len(errs))
	}
	got := map[string]AlignmentError{}
	for _, e := range errs {
		got[e.Key] = e
	}
	if got["k1"].Error != 150 || got["k2"].Error != 100 {
		t.Fatalf("signed errors wrong: %+v %+v", got["k1"], got["k2"])
	}
	if !got["k3"].Missing {
		t.Fatal("k3 must be marked missing")
	}
	max, missing := MaxAbsAlignmentError(errs)
	if max != 150 || missing != 1 {
		t.Fatalf("max=%d missing=%d, want 150/1", max, missing)
	}
}

func TestReportDeterministicJSON(t *testing.T) {
	var frames []model.Sample
	for seq := uint32(0); seq < 4; seq++ {
		frames = append(frames, model.Sample{
			Seq: seq, Tick: clock.Tick(seq) * fixture.VideoFrameTick,
			Width: fixture.FrameW, Height: fixture.FrameH,
			Frame: fixture.FramePixels(7, seq),
		})
	}
	var audio []model.Sample
	for seq := uint32(0); seq < 2; seq++ {
		audio = append(audio, model.Sample{
			Seq: seq, Tick: clock.Tick(seq) * fixture.VideoFrameTick,
			Audio: fixture.AudioSamples(7, seq),
		})
	}
	tracks := []model.Track{
		{ID: "v", Kind: model.KindVideo, Samples: frames},
		{ID: "a", Kind: model.KindAudio, Samples: audio},
	}
	r1, err := Build(tracks, &timeline.Timeline{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Build(tracks, &timeline.Timeline{})
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := r1.Marshal()
	b2, _ := r2.Marshal()
	if string(b1) != string(b2) {
		t.Fatal("evidence JSON not byte-deterministic")
	}
	// JSON must be valid and carry the required evidence sections.
	var parsed map[string]any
	if err := json.Unmarshal(b1, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"frames", "audio", "alignment",
		"max_alignment_error_tick", "missing_events"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("report missing field %s", key)
		}
	}
	if summary := r1.Summary(); summary == "" ||
		!contains(summary, "peak_dbfs") || !contains(summary, "first_sha256") {
		t.Fatalf("summary missing required evidence:\n%s", summary)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
