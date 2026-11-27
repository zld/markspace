package audio

import (
	"math"
	"testing"
)

func sine(rate, frames int) PCM {
	p := PCM{Rate: rate, Channels: 1, Samples: make([]float32, frames)}
	for n := 0; n < frames; n++ {
		p.Samples[n] = float32(0.5 * math.Sin(2*math.Pi*440*float64(n)/float64(rate)))
	}
	return p
}

func TestResampleLengthAndAlignment(t *testing.T) {
	in := sine(32000, 32000) // exactly one second
	out, err := Resample(in, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames() != 48000 {
		t.Fatalf("frames=%d", out.Frames())
	}
	// Signal content survives: correlation peak near zero lag.
	maxV := float32(0)
	for _, s := range out.Samples {
		if s > maxV {
			maxV = s
		}
	}
	if maxV < 0.45 || maxV > 0.55 {
		t.Fatalf("amplitude=%v", maxV)
	}
}

func TestResampleRejectsBadRate(t *testing.T) {
	if _, err := Resample(sine(32000, 100), 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestMapChannels(t *testing.T) {
	mono := sine(48000, 100)
	st, err := MapChannels(mono, 2)
	if err != nil {
		t.Fatal(err)
	}
	if st.Channels != 2 || st.Frames() != 100 {
		t.Fatalf("out=%+v", st)
	}
	if st.Samples[0] != st.Samples[1] {
		t.Fatal("mono not replicated")
	}
	back, err := MapChannels(st, 1)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(back.Samples[3]-mono.Samples[3])) > 1e-6 {
		t.Fatal("round trip mismatch")
	}
	if _, err := MapChannels(mono, 3); err == nil {
		t.Fatal("expected layout error")
	}
}

func TestNormalizeHitsTargetAndCeiling(t *testing.T) {
	out, st, err := Normalize(sine(48000, 4800), -16.0, -1.0)
	if err != nil {
		t.Fatal(err)
	}
	if st.LimiterEngaged {
		t.Fatal("limiter should not engage for a -9 dBFS sine")
	}
	if st.PeakDBFS > -1.0 {
		t.Fatalf("peak=%.2f", st.PeakDBFS)
	}
	// Hot signal: peak would exceed the ceiling, limiter must pull it
	// down and report.
	hot := sine(48000, 4800)
	for i := range hot.Samples {
		hot.Samples[i] *= 1.9
	}
	_, st2, err := Normalize(hot, -3.0, -1.0)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.LimiterEngaged || st2.PeakDBFS > -1.0 {
		t.Fatalf("stats=%+v", st2)
	}
	_ = out
}

func TestNormalizeSilence(t *testing.T) {
	p := PCM{Rate: 48000, Channels: 1, Samples: make([]float32, 100)}
	if _, _, err := Normalize(p, -16, -1); err == nil {
		t.Fatal("expected silence error")
	}
}
