package normalize

import (
	"errors"
	"testing"

	"mediadist/timeline"
)

func audioTrack(layout timeline.ChannelLayout, rate uint32, entries uint32) *timeline.Track {
	clock, _ := timeline.NewClock(rate)
	dur := clock.ToCommon(uint64(entries) * 1024)
	return &timeline.Track{
		ID: 2, Kind: timeline.KindAudio, Timescale: rate,
		Layout: layout, SampleRate: rate,
		Samples: []timeline.TrackSample{{DTS: 0, CTS: 0, Dur: dur}},
	}
}

func TestPlanChannelMappings(t *testing.T) {
	cases := []struct {
		layout    timeline.ChannelLayout
		outCh     int
		wantErr   bool
		mixChecks int // number of output mixes
	}{
		{timeline.LayoutMono, 2, false, 2},
		{timeline.LayoutStereo, 2, false, 2},
		{timeline.LayoutFiveOne, 2, false, 2},
		{timeline.LayoutUnsupported, 0, true, 0},
	}
	for _, tc := range cases {
		cm, err := PlanChannel(tc.layout)
		if tc.wantErr {
			var le *timeline.LocError
			if !errors.As(err, &le) || le.Code != timeline.CodeBadLayout {
				t.Fatalf("%v: got %v want LAYOUT_UNSUPPORTED", tc.layout, err)
			}
			continue
		}
		if err != nil || cm.OutLayout.Channels() != tc.outCh || len(cm.Mixes) != tc.mixChecks {
			t.Fatalf("%v: map=%+v err=%v", tc.layout, cm, err)
		}
	}
	// 5.1 downmix must reference C and surrounds and exclude LFE (index 3).
	cm, _ := PlanChannel(timeline.LayoutFiveOne)
	used := map[int]bool{}
	for _, m := range cm.Mixes {
		for _, sg := range m.Sources {
			used[sg.SourceIndex] = true
		}
	}
	if used[3] {
		t.Fatal("LFE must not be folded into stereo")
	}
	if !used[2] || !used[4] || !used[5] {
		t.Fatalf("center/surrounds missing from downmix: %v", used)
	}
}

func TestPlanAudioSampleRateAndCounts(t *testing.T) {
	// 12 s of 44.1 kHz stereo -> 48 kHz stereo.
	tr := audioTrack(timeline.LayoutStereo, 44100, 12*43) // ~12 s of 1024-frame entries
	plan, err := PlanAudio(tr, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RateIn != 44100 || plan.RateOut != 48000 {
		t.Fatalf("rate conversion wrong: %+v", plan)
	}
	if plan.ChannelsIn != 2 || plan.ChannelsOut != 2 {
		t.Fatalf("channel counts wrong: %+v", plan)
	}
	if plan.SamplesOut <= plan.SamplesIn {
		t.Fatalf("48k output must have more frames: in=%d out=%d", plan.SamplesIn, plan.SamplesOut)
	}
	// Rounding delta must never exceed one input sample window.
	if plan.DeltaTicks > 90000/44100+1 {
		t.Fatalf("duration delta %d exceeds one source tick", plan.DeltaTicks)
	}
	t.Logf("in=%d out=%d delta_ticks=%d", plan.SamplesIn, plan.SamplesOut, plan.DeltaTicks)
}

func TestPlanAudioFiveOne(t *testing.T) {
	tr := audioTrack(timeline.LayoutFiveOne, 48000, 100)
	plan, err := PlanAudio(tr, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.LayoutIn != timeline.LayoutFiveOne || plan.LayoutOut != timeline.LayoutStereo ||
		plan.ChannelsIn != 6 || plan.ChannelsOut != 2 {
		t.Fatalf("5.1 normalization wrong: %+v", plan)
	}
	if plan.SamplesIn != plan.SamplesOut {
		t.Fatalf("same-rate conversion must preserve frame count: %+v", plan)
	}
}

func videoTrack(cands map[timeline.ColorSource]timeline.ColorDesc) *timeline.Track {
	return &timeline.Track{ID: 1, Kind: timeline.KindVideo, Timescale: 90000,
		ColorCandidates: cands,
		Samples:         []timeline.TrackSample{{DTS: 0, CTS: 0, Dur: 90000}}}
}

func TestArbitrateColorPriorityAndConflict(t *testing.T) {
	// Same values, two sources: no conflict, bitstream wins by default priority.
	same := timeline.ColorDesc{Primaries: 9, Transfer: 16, Matrix: 9}
	cands := map[timeline.ColorSource]timeline.ColorDesc{
		timeline.SourceContainer: same,
		timeline.SourceBitstream: same,
	}
	dec, err := ArbitrateColor(videoTrack(cands), nil, nil)
	if err != nil || !dec.Resolved || dec.Source != timeline.SourceBitstream {
		t.Fatalf("arbitration wrong: dec=%+v err=%v", dec, err)
	}

	// Conflicting values: COLOR_CONFLICT with the track locator.
	conflict := map[timeline.ColorSource]timeline.ColorDesc{
		timeline.SourceContainer: same,
		timeline.SourceBitstream: {Primaries: 1, Transfer: 1, Matrix: 1},
	}
	_, err = ArbitrateColor(videoTrack(conflict), nil, nil)
	var le *timeline.LocError
	if !errors.As(err, &le) || le.Code != timeline.CodeColorConflict || le.TrackID != 1 {
		t.Fatalf("got %v, want COLOR_CONFLICT on track 1", err)
	}

	// CLI override adjudicates the same conflict deterministically.
	cli := &timeline.ColorDesc{Primaries: 9, Transfer: 16, Matrix: 9}
	dec, err = ArbitrateColor(videoTrack(conflict), cli, nil)
	if err != nil || dec.Source != timeline.SourceCLI || !dec.Resolved {
		t.Fatalf("CLI override failed: %+v %v", dec, err)
	}
}

func TestArbitrateColorPendingBlocksPublish(t *testing.T) {
	dec, err := ArbitrateColor(videoTrack(map[timeline.ColorSource]timeline.ColorDesc{}), nil, nil)
	if err != nil || !dec.Pending || dec.Resolved {
		t.Fatalf("missing color must stay pending: %+v %v", dec, err)
	}
	if err := RequireResolved(dec); err == nil {
		t.Fatal("pending decision must block publication")
	} else {
		var le *timeline.LocError
		if !errors.As(err, &le) || le.Code != timeline.CodeColorPending {
			t.Fatalf("got %v want COLOR_PENDING", err)
		}
	}
}
