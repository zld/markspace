package silence

import (
	"math"
	"testing"

	"mediad/internal/splitter"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestDetectBasicInterval(t *testing.T) {
	frames := []PCMFrame{
		{TrackID: 2, StartUS: 0, EndUS: 1000, Samples: []float64{0.01, -0.02}},
		{TrackID: 2, StartUS: 1000, EndUS: 2000, Samples: []float64{0.0, 0.015}},
		{TrackID: 2, StartUS: 2000, EndUS: 3000, Samples: []float64{0.9, -0.8}},
	}
	out := Detect(frames, Config{AmplitudeThreshold: 0.05, MinDurationUS: 1500})
	if len(out) != 1 {
		t.Fatalf("want 1 interval, got %d: %+v", len(out), out)
	}
	iv := out[0]
	if iv.StartUS != 0 || iv.EndUS != 2000 {
		t.Fatalf("bounds wrong: %+v", iv)
	}
	if !approx(iv.Peak, 0.02, 1e-9) {
		t.Fatalf("peak = %v, want 0.02", iv.Peak)
	}
	// RMS of {0.01,-0.02,0,0.015}: sqrt((1+4+0+2.25)e-4/4).
	wantRMS := math.Sqrt((0.0001 + 0.0004 + 0 + 0.000225) / 4)
	if !approx(iv.RMS, wantRMS, 1e-12) {
		t.Fatalf("rms = %v, want %v", iv.RMS, wantRMS)
	}
}

func TestDetectMinDurationFiltersShort(t *testing.T) {
	frames := []PCMFrame{
		{StartUS: 0, EndUS: 500, Samples: []float64{0.0}},
		{StartUS: 500, EndUS: 1500, Samples: []float64{0.8}},
		{StartUS: 1500, EndUS: 4000, Samples: []float64{0.01}},
	}
	out := Detect(frames, Config{AmplitudeThreshold: 0.05, MinDurationUS: 1000})
	if len(out) != 1 || out[0].StartUS != 1500 {
		t.Fatalf("short silence must be filtered, got %+v", out)
	}
}

func TestDetectInvalidFrameBreaksInterval(t *testing.T) {
	frames := []PCMFrame{
		{StartUS: 0, EndUS: 1000, Samples: []float64{0.0}},
		{StartUS: 1000, EndUS: 2000, Invalid: true, Samples: []float64{0.0}},
		{StartUS: 2000, EndUS: 4000, Samples: []float64{0.0}},
	}
	out := Detect(frames, Config{AmplitudeThreshold: 0.05, MinDurationUS: 1000})
	// The invalid frame splits one long region into two 1000us halves; the
	// interval must never span the invalid middle frame.
	for _, iv := range out {
		if iv.StartUS < 1000 && iv.EndUS > 2000 {
			t.Fatalf("silence spans invalid interval: %+v", iv)
		}
	}
	if len(out) != 2 {
		t.Fatalf("want two separated intervals, got %d: %+v", len(out), out)
	}
}

func TestDetectFromMapped(t *testing.T) {
	mapped := []splitter.MappedSample{
		{TrackID: 1, Index: 0, StartUS: 0, EndUS: 1000},
		{TrackID: 1, Index: 1, StartUS: 1000, EndUS: 3000, Invalid: true},
	}
	amps := map[int][]float64{
		0: {0.0, 0.0},
		1: {0.0, 0.0},
	}
	out := DetectFromMapped(mapped, amps, Config{AmplitudeThreshold: 0.05, MinDurationUS: 500})
	if len(out) != 1 || out[0].EndUS != 1000 {
		t.Fatalf("invalid mapped sample must break silence: %+v", out)
	}
}
