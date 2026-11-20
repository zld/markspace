package plc

import (
	"math"
	"testing"
)

func toneFrame(amp float64) []byte {
	s := make([]int16, FrameSamples)
	for i := range s {
		s[i] = int16(amp * math.Sin(2*math.Pi*450*float64(i)/SampleRate))
	}
	return PCMFromSamples(s)
}

func TestSamplesRoundTrip(t *testing.T) {
	pcm := toneFrame(10000)
	got := SamplesFromPCM(pcm)
	if len(got) != FrameSamples {
		t.Fatalf("samples %d", len(got))
	}
	if got[10] != int16(10000*math.Sin(2*math.Pi*450*10/SampleRate)) {
		t.Fatalf("sample mismatch: %d", got[10])
	}
}

func TestConcealDecaysAndMutes(t *testing.T) {
	c := NewConcealer(3)
	c.NoteGood(toneFrame(10000))
	var lastPeak int
	for miss := 1; miss <= 6; miss++ {
		frame := SamplesFromPCM(c.Conceal())
		peak := 0
		for _, v := range frame {
			if a := int(math.Abs(float64(v))); a > peak {
				peak = a
			}
		}
		if miss > 3 && peak != 0 {
			t.Fatalf("miss %d: expected silence, peak %d", miss, peak)
		}
		if miss <= 3 && peak >= lastPeak && miss > 1 {
			t.Fatalf("miss %d: peak did not decay (%d >= %d)", miss, peak, lastPeak)
		}
		lastPeak = peak
	}
}

func TestConcealBeforeAnyGoodFrameIsSilence(t *testing.T) {
	c := NewConcealer(2)
	for _, v := range SamplesFromPCM(c.Conceal()) {
		if v != 0 {
			t.Fatal("expected silence")
		}
	}
}

func TestBoundaryTracker(t *testing.T) {
	bt := NewBoundaryTracker()
	a := toneFrame(10000)
	b := toneFrame(10000)
	bt.Observe(a, b, false)
	bt.Observe(a, b, true)
	if pct := bt.ContinuityPct(); pct != 100 {
		t.Fatalf("continuity %v", pct)
	}
	// Discontinuous pair: silence then a loud DC frame.
	sil := make([]byte, FrameBytes)
	dc := make([]int16, FrameSamples)
	for i := range dc {
		dc[i] = 12000
	}
	bt.Observe(sil, PCMFromSamples(dc), true)
	if pct := bt.ContinuityPct(); pct != 50 {
		t.Fatalf("continuity %v, want 50", pct)
	}
	if p95 := bt.JumpP95(); p95 <= 0 {
		t.Fatalf("p95 %d", p95)
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatalf("got %d", got)
	}
}
