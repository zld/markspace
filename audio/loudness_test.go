package audio

import (
	"math"
	"testing"
)

func TestMeasureAndApplyGain(t *testing.T) {
	c, err := NewConverter(Config{
		SourceRate: 48000, SourceLayout: "stereo",
		TargetRate: 48000, TargetLayout: "stereo",
		LoudnessLUFS: -16,
	})
	if err != nil {
		t.Fatal(err)
	}
	cv := c.Start(48000)
	// A constant 0.25 full scale sine-free signal measures around -12 dBFS.
	block := stereoBlock(48000, 8192)
	if _, err := cv.ConvertBlock(block, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	measured := cv.InputLoudness()
	if measured <= -70 || measured > 0 {
		t.Fatalf("measured loudness out of range: %.2f", measured)
	}
	if err := c.ApplyGain(measured); err != nil {
		t.Fatal(err)
	}
	if math.Abs(c.GainDB-(c.TargetLUFS-measured)) > 1e-9 {
		t.Fatalf("gain %.4f want %.4f", c.GainDB, c.TargetLUFS-measured)
	}
}

func TestApplyGainDisabledWithoutTarget(t *testing.T) {
	c, err := NewConverter(Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyGain(-20); err != nil {
		t.Fatal(err)
	}
	if c.GainDB != 0 {
		t.Fatalf("gain without target must be zero, got %.2f", c.GainDB)
	}
}

func TestApplyGainRejectsSilence(t *testing.T) {
	c, err := NewConverter(Config{
		SourceRate: 48000, SourceLayout: "stereo",
		TargetRate: 48000, TargetLayout: "stereo", LoudnessLUFS: -16,
	})
	if err != nil {
		t.Fatal(err)
	}
	cv := c.Start(48000)
	if _, err := cv.ConvertBlock(stereoBlock(48000, 0), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyGain(cv.InputLoudness()); err == nil {
		t.Fatal("silent input must not be normalised")
	}
}

func TestApplyGainLimit(t *testing.T) {
	c, err := NewConverter(Config{
		SourceRate: 48000, SourceLayout: "stereo",
		TargetRate: 48000, TargetLayout: "stereo", LoudnessLUFS: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyGain(-69); err == nil {
		t.Fatal("gain above 60 dB must be rejected")
	}
}

func TestLoudnessTargetAboveFullScaleRejected(t *testing.T) {
	if _, err := NewConverter(Config{
		SourceRate: 48000, SourceLayout: "stereo",
		TargetRate: 48000, TargetLayout: "stereo", LoudnessLUFS: 3,
	}); err == nil {
		t.Fatal("positive loudness target must fail")
	}
}

func TestLoudnessDeterministic(t *testing.T) {
	measure := func() float64 {
		c, err := NewConverter(Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
		if err != nil {
			t.Fatal(err)
		}
		cv := c.Start(8000)
		if _, err := cv.ConvertBlock(stereoBlock(8000, 4000), 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		return cv.InputLoudness()
	}
	a, b := measure(), measure()
	if a != b {
		t.Fatalf("non-deterministic loudness %.6f != %.6f", a, b)
	}
}

func TestGainAppliedToOutput(t *testing.T) {
	c, err := NewConverter(Config{SourceRate: 48000, SourceLayout: "stereo", TargetRate: 48000, TargetLayout: "stereo"})
	if err != nil {
		t.Fatal(err)
	}
	c.GainDB = 6.0206 // approximately x2
	cv := c.Start(2)
	out, err := cv.ConvertBlock(stereoBlock(2, 1000), 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 1000 * 2.0000 rounds to 2000.
	got := int16(uint16(out.PCM[0]) | uint16(out.PCM[1])<<8)
	if got != 2000 {
		t.Fatalf("gained sample %d want 2000", got)
	}
}

func TestClamp16(t *testing.T) {
	cases := []struct {
		in   float64
		want int16
	}{
		{0.4, 0}, {0.5, 1}, {-0.5, -1}, {32767.6, 32767}, {-32768.7, -32768}, {100000, 32767},
	}
	for _, c := range cases {
		if got := clamp16(c.in); got != c.want {
			t.Fatalf("clamp16(%v)=%d want %d", c.in, got, c.want)
		}
	}
}
