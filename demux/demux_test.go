package demux

import (
	"errors"
	"testing"

	"mediadist/timeline"
)

func demoSpec(fault FaultCase) FixtureSpec {
	spec := FixtureSpec{
		Seed: 17, Duration: 12,
		Video: VideoSpec{
			Timescale: 24000, Width: 1280, Height: 720,
			KeyframeEvery: 48, FrameDelta: 1001, FrameCount: 288,
			Color: &ColorValue{Primaries: 9, Transfer: 16, Matrix: 9},
		},
		Audio: AudioSpec{
			Timescale: 48000, SampleRate: 48000, Channels: 2,
			FrameSamples: 1024, FrameCount: 562,
		},
		Fault: fault,
	}
	if fault == FaultColorConflict {
		spec.Video.BitstreamColor = &ColorValue{Primaries: 1, Transfer: 1, Matrix: 1}
	}
	return spec
}

func TestSmokeRoundTrip(t *testing.T) {
	data, err := WriteFixture(demoSpec(FaultNone))
	if err != nil {
		t.Fatal(err)
	}
	g, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := g.ByKind(timeline.KindVideo)
	a := g.ByKind(timeline.KindAudio)
	if v == nil || a == nil || len(v.Samples) != 288 || len(a.Samples) != 562 {
		t.Fatalf("sample counts wrong v=%d a=%d", len(v.Samples), len(a.Samples))
	}
	if a.Layout != timeline.LayoutStereo || a.SampleRate != 48000 {
		t.Fatalf("audio metadata wrong layout=%v rate=%d", a.Layout, a.SampleRate)
	}
	if err := timeline.ValidateGraph(g); err != nil {
		t.Fatalf("validate: %v", err)
	}
	t.Logf("ok video_end=%d audio_end=%d colors=%d", v.End(), a.End(), len(v.ColorCandidates))
}

func TestSmokeAllFaults(t *testing.T) {
	cases := []struct {
		fault FaultCase
		code  string
		stage string
	}{
		{FaultTruncatedIndex, timeline.CodeTruncatedIndex, timeline.StageDemux},
		{FaultTimestampBack, timeline.CodeTimestampBack, timeline.StageTimeline},
		{FaultBadLayout, timeline.CodeBadLayout, timeline.StageTimeline},
		{FaultMissingKeyframe, timeline.CodeMissingKeyframe, timeline.StageSegment},
	}
	for _, tc := range cases {
		t.Run(string(tc.fault), func(t *testing.T) {
			data, err := WriteFixture(demoSpec(tc.fault))
			if err != nil {
				t.Fatal(err)
			}
			g, perr := ParseBytes(data)
			if perr != nil {
				if tc.stage != timeline.StageDemux {
					t.Fatalf("unexpected parse failure: %v", perr)
				}
				var le *timeline.LocError
				if !errors.As(perr, &le) || le.Code != tc.code {
					t.Fatalf("got %v want %s", perr, tc.code)
				}
				return
			}
			InjectGraphFault(g, tc.fault)
			if verr := timeline.ValidateGraph(g); verr != nil && tc.stage == timeline.StageTimeline {
				var le *timeline.LocError
				if !errors.As(verr, &le) || le.Code != tc.code {
					t.Fatalf("got %v want %s", verr, tc.code)
				}
				return
			} else if verr != nil {
				t.Fatalf("unexpected validation error: %v", verr)
			}
			if tc.fault == FaultMissingKeyframe {
				keys := 0
				for _, s := range g.ByKind(timeline.KindVideo).Samples {
					if s.Keyframe {
						keys++
					}
				}
				if keys != 1 {
					t.Fatalf("want only 1 keyframe, got %d", keys)
				}
			}
		})
	}
}

func TestSmokeColorConflict(t *testing.T) {
	data, err := WriteFixture(demoSpec(FaultColorConflict))
	if err != nil {
		t.Fatal(err)
	}
	g, err := ParseBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	v := g.ByKind(timeline.KindVideo)
	if len(v.ColorCandidates) < 1 {
		t.Fatal("color candidates missing")
	}
	t.Logf("color candidates=%d", len(v.ColorCandidates))
}

func TestSmokeRepair(t *testing.T) {
	data, _ := WriteFixture(demoSpec(FaultTimestampBack))
	g, err := ParseBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	InjectGraphFault(g, FaultTimestampBack)
	if err := timeline.ValidateGraph(g); err == nil {
		t.Fatal("expected backward timestamp before repair")
	}
	res, err := RepairGraph(g)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(res.Recovered) != 1 || res.Recovered[0] != timeline.CodeTimestampBack {
		t.Fatalf("repair result wrong: %+v", res)
	}
	if err := timeline.ValidateGraph(g); err != nil {
		t.Fatalf("post-repair validation: %v", err)
	}
}

func TestParseRejectsOversizedAndEmpty(t *testing.T) {
	if _, err := ParseBytes(nil); err == nil {
		t.Fatal("empty input must be rejected")
	}
	big := make([]byte, MaxInputBytes+1)
	if _, err := ParseBytes(big); err == nil {
		t.Fatal("oversized input must be rejected")
	} else {
		var le *timeline.LocError
		if !errors.As(err, &le) || le.Code != timeline.CodeUncheckedInput {
			t.Fatalf("got %v, want INPUT_LIMIT_EXCEEDED", err)
		}
	}
}

func TestRepairUnsupportedLayout(t *testing.T) {
	g := &timeline.Graph{}
	tr := &timeline.Track{
		ID: 9, Kind: timeline.KindAudio, Timescale: 48000,
		Layout: timeline.LayoutUnsupported, SampleRate: 48000,
		Samples: []timeline.TrackSample{{DTS: 0, CTS: 0, Dur: 1024}},
	}
	g.Tracks = append(g.Tracks, tr)
	res, err := RepairGraph(g)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Layout != timeline.LayoutStereo || len(res.Recovered) != 1 {
		t.Fatalf("layout repair wrong: %+v layout=%v", res, tr.Layout)
	}
	if !IsRecoverable(timeline.CodeTimestampBack) || IsRecoverable(timeline.CodeMissingKeyframe) {
		t.Fatal("recoverability classification wrong")
	}
}
