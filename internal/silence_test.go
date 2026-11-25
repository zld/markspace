package kernel

import "testing"

func pcmFromAmp(amp int16) []byte {
	b := make([]byte, 20)
	for i := 0; i < 10; i++ {
		b[2*i] = byte(uint16(amp))
		b[2*i+1] = byte(uint16(amp) >> 8)
	}
	return b
}

func silenceFixtureTrack() *Track {
	tr := &Track{ID: "audio-test", PID: 0x0101, Kind: TrackAudio}
	for tMS := int64(0); tMS < 6000; tMS += 100 {
		amp := int16(12000)
		if tMS >= 1000 && tMS < 2000 {
			amp = 0
		}
		tr.Samples = append(tr.Samples, Sample{
			Index: uint64(tMS / 100), PTS90k: uint64(tMS) * 90, Duration90k: 9000,
			Data: pcmFromAmp(amp),
		})
	}
	tr.Start90k = 0
	tr.End90k = 600 * 9000
	return tr
}

func TestSilenceDetectsConfiguredMinimum(t *testing.T) {
	intervals, err := DetectSilence(silenceFixtureTrack(), nil, SilenceConfig{ThresholdAmplitude: 256, MinDurationMS: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(intervals) != 1 {
		t.Fatalf("intervals=%#v", intervals)
	}
	if intervals[0].Time != RangeMS(1000, 2000) {
		t.Fatalf("range=%#v", intervals[0].Time)
	}
	if got := SilenceTotalMS(intervals); got != 1000 {
		t.Fatalf("silence ms=%d want 1000", got)
	}
}

func TestSilenceGapCoveredRegionIsExcluded(t *testing.T) {
	gaps := []GapEvent{{ID: "g1", Kind: GapCCJump, PID: 0x0101, Time: RangeMS(1000, 2000), Recoverable: true}}
	intervals, err := DetectSilence(silenceFixtureTrack(), gaps, SilenceConfig{ThresholdAmplitude: 256, MinDurationMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(intervals) != 0 {
		t.Fatalf("gap-covered silence must not be reported: %#v", intervals)
	}

	partial := []GapEvent{{ID: "g2", Time: RangeMS(1200, 1500)}}
	intervals, err = DetectSilence(silenceFixtureTrack(), partial, SilenceConfig{ThresholdAmplitude: 256, MinDurationMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(intervals) != 2 {
		t.Fatalf("expected two uncovered pieces, got %#v", intervals)
	}
	if intervals[0].Time != RangeMS(1000, 1200) || intervals[1].Time != RangeMS(1500, 2000) {
		t.Fatalf("unexpected pieces: %#v", intervals)
	}
}
