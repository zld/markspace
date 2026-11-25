package kernel

import "testing"

func TestUnknownFixtureIsFatal(t *testing.T) {
	_, err := GenerateFixture("bogus", false)
	if ExitCodeOf(err) != 1 {
		t.Fatalf("unknown fixture err=%v exit=%d", err, ExitCodeOf(err))
	}
}

func TestSilenceThresholdZeroMeansExactZero(t *testing.T) {
	tr := &Track{ID: "tiny", PID: 1, Kind: TrackAudio, Samples: []Sample{{
		PTS90k: 0, Duration90k: 90_000, Data: pcmFromAmp(1),
	}}}
	none, err := DetectSilence(tr, nil, SilenceConfig{ThresholdAmplitude: 0, MinDurationMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("amplitude 1 should not be silent at threshold 0: %#v", none)
	}
	quiet, err := DetectSilence(tr, nil, SilenceConfig{ThresholdAmplitude: 1, MinDurationMS: 1})
	if err != nil || len(quiet) != 1 {
		t.Fatalf("threshold 1 should accept amplitude 1: intervals=%#v err=%v", quiet, err)
	}
}

func TestSubtractGapsAttributesNestedOverlappingIDs(t *testing.T) {
	r := TimeRange{Start90k: 0, End90k: 1000}
	gaps := []GapEvent{
		{ID: "outer", Time: TimeRange{Start90k: 100, End90k: 900}},
		{ID: "inner", Time: TimeRange{Start90k: 200, End90k: 300}},
	}
	pieces, ids := SubtractGapsWithIDs(r, gaps)
	if len(pieces) != 2 {
		t.Fatalf("pieces=%#v", pieces)
	}
	if len(ids) != 2 {
		t.Fatalf("ids=%v want outer and inner", ids)
	}
}
