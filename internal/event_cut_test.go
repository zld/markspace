package kernel

import "testing"

func TestCutProgramUsesHalfOpenRanges(t *testing.T) {
	audio := silenceFixtureTrack()
	prog := &NormalizedProgram{
		Duration90k: RangeMS(0, 6000).End90k,
		Tracks:      []*Track{audio},
		Events:      []NormalizedEvent{{ID: "e1", Label: "cut", PTS90k: RangeMS(3000, 3000).Start90k}},
		Gaps:        []GapEvent{{ID: "gap-1", Time: RangeMS(1500, 1600), Recoverable: true}},
	}
	segs, err := CutProgram(prog, CutConfig{Silence: SilenceConfig{ThresholdAmplitude: 256, MinDurationMS: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("segments=%d want 2", len(segs))
	}
	if segs[0].Time != RangeMS(0, 3000) || segs[1].Time != RangeMS(3000, 6000) {
		t.Fatalf("ranges=%#v %#v", segs[0].Time, segs[1].Time)
	}
	first := segs[0].Tracks[0]
	second := segs[1].Tracks[0]
	if len(first.Samples) != 30 || len(second.Samples) != 30 {
		t.Fatalf("sample counts first=%d second=%d", len(first.Samples), len(second.Samples))
	}
	if first.Samples[len(first.Samples)-1].PTS90k != RangeMS(2900, 2900).Start90k {
		t.Fatal("boundary sample leaked into closed side")
	}
	if second.Samples[0].PTS90k != RangeMS(3000, 3000).Start90k {
		t.Fatal("boundary sample missing from half-open opening side")
	}
	if len(segs[0].GapIDs) != 1 || segs[0].GapIDs[0] != "gap-1" {
		t.Fatalf("gap refs=%#v", segs[0].GapIDs)
	}
}

func TestCutProgramPSIIncompleteIsRecoverable(t *testing.T) {
	psi := NewPSITracker()
	psi.Current = &PSIMap{PMTVersion: 1, TrackPIDs: map[uint16]TrackKind{fixtureAudioPID: TrackAudio}}
	psi.pendingPMT = &pendingPSI{version: 2, last: 1, sections: map[uint8]*PSISection{}}
	psi.PausedPIDs[fixtureAudioPID] = struct{}{}
	prog := &NormalizedProgram{Duration90k: 100, Tracks: []*Track{{PID: fixtureAudioPID, Kind: TrackAudio}}}
	_, err := CutProgram(prog, CutConfig{PSI: psi})
	if ExitCodeOf(err) != 2 {
		t.Fatalf("expected recoverable exit 2, got %v", err)
	}
}
