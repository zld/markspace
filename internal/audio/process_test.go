package audio

import (
	"math"
	"testing"

	"mediacore/internal/timeline"
)

func sineInput(t *testing.T, layout Layout, frames, rate int) SegmentInput {
	t.Helper()
	b, err := NewBuffer(layout, frames)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < frames; i++ {
		v := 0.2 * math.Sin(2*math.Pi*440*float64(i)/float64(rate))
		for c := 0; c < layout.NumChannels(); c++ {
			b.Data[i*layout.NumChannels()+c] = v
		}
	}
	return SegmentInput{Buffer: b, StartTicks: 0}
}

func TestProcessConvertsRateAndLayoutInOnePass(t *testing.T) {
	p := SegmentProcessor{
		InRate: 44100, OutRate: 48000,
		InLayout: Mono, OutLayout: Stereo,
		Gain: GainRequest{TargetLUFS: -16, PeakDBFS: -1},
	}
	res, err := p.Process([]SegmentInput{sineInput(t, Mono, 44100, 44100)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 {
		t.Fatalf("segments = %d", len(res.Segments))
	}
	seg := res.Segments[0]
	if seg.Buffer.Layout.Name != "stereo" {
		t.Fatalf("layout = %s", seg.Buffer.Layout.Name)
	}
	if seg.Buffer.Frames != 48000 {
		t.Fatalf("frames = %d, want 48000", seg.Buffer.Frames)
	}
	if math.Abs(res.Output.IntegratedLUFS-(-16)) > 1.5 {
		t.Fatalf("output loudness %0.2f misses target", res.Output.IntegratedLUFS)
	}
	if res.Stats.Resets != 1 || res.Stats.Segments != 1 {
		t.Fatalf("stats = %+v", res.Stats)
	}
	if err := res.Stats.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessKeepsGapsAndCountsOneResetPerSegment(t *testing.T) {
	p := SegmentProcessor{
		InRate: 48000, OutRate: 48000,
		InLayout: Stereo, OutLayout: Stereo,
		Gain: GainRequest{TargetLUFS: -16, PeakDBFS: -1},
	}
	a := sineInput(t, Stereo, 4800, 48000)
	b := sineInput(t, Stereo, 4800, 48000)
	b.StartTicks = timeline.Ticks(9600) // a 100 ms hole after the first run
	res, err := p.Process([]SegmentInput{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(res.Segments))
	}
	if res.Segments[1].StartTicks != 9600 {
		t.Fatalf("second segment start = %d, want 9600", res.Segments[1].StartTicks)
	}
	if res.Stats.Resets != 2 {
		t.Fatalf("resets = %d, want one per segment (2)", res.Stats.Resets)
	}
	if res.Stats.Segments != 2 {
		t.Fatalf("segments stat = %d", res.Stats.Segments)
	}
}

func TestProcessSilentInputIsFinite(t *testing.T) {
	p := SegmentProcessor{
		InRate: 48000, OutRate: 48000,
		InLayout: Stereo, OutLayout: Stereo,
		Gain: GainRequest{TargetLUFS: -16, PeakDBFS: -1},
	}
	silent, _ := NewBuffer(Stereo, 4800)
	res, err := p.Process([]SegmentInput{{Buffer: silent}})
	if err != nil {
		t.Fatal(err)
	}
	if res.GainDB != 0 || res.Limited {
		t.Fatalf("silent gain changed: %+v", res)
	}
	if math.IsNaN(res.Output.IntegratedLUFS) || math.IsInf(res.Output.IntegratedLUFS, 0) {
		t.Fatal("silent output loudness is non-finite")
	}
	if res.Output.IntegratedLUFS != SilenceLUFS {
		t.Fatalf("silent loudness = %v", res.Output.IntegratedLUFS)
	}
}

func TestProcessRejectsMismatchedLayout(t *testing.T) {
	p := SegmentProcessor{
		InRate: 48000, OutRate: 48000,
		InLayout: Stereo, OutLayout: Stereo,
		Gain: GainRequest{TargetLUFS: -16, PeakDBFS: -1},
	}
	seg := sineInput(t, Mono, 480, 48000)
	if _, err := p.Process([]SegmentInput{seg}); err == nil {
		t.Fatal("mismatched segment layout accepted")
	}
}

func TestProcessEmptyInputIsStable(t *testing.T) {
	p := SegmentProcessor{
		InRate: 48000, OutRate: 48000,
		InLayout: Stereo, OutLayout: Stereo,
		Gain: GainRequest{TargetLUFS: -16, PeakDBFS: -1},
	}
	res, err := p.Process(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 0 {
		t.Fatalf("segments = %d", len(res.Segments))
	}
	if res.Stats.OutputFrames != 0 {
		t.Fatalf("frames = %d", res.Stats.OutputFrames)
	}
}
