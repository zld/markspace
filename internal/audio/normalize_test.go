package audio

import (
	"math"
	"testing"
)

func TestResampleFrameCount(t *testing.T) {
	cases := []struct{ inRate, outRate, inFrames int }{
		{44100, 48000, 44100},
		{48000, 48000, 48000},
		{48000, 8000, 48000},
		{96000, 48000, 12345},
	}
	for _, c := range cases {
		r, err := NewResampler(c.inRate, c.outRate, 2)
		if err != nil {
			t.Fatal(err)
		}
		in := make([]float64, c.inFrames*2)
		out := r.Process(in)
		out = append(out, r.Flush()...)
		got := int64(len(out) / 2)
		want := ExpectedOut(int64(c.inFrames), c.inRate, c.outRate)
		if d := got - want; d < -1 || d > 1 {
			t.Fatalf("%v: got %d frames, theory %d", c, got, want)
		}
	}
}

func TestResampleStreamingEqualsOneShot(t *testing.T) {
	const n = 10000
	in := make([]float64, n)
	for i := range in {
		in[i] = math.Sin(float64(i) * 0.01)
	}
	one, _ := NewResampler(44100, 48000, 1)
	full := one.Process(in)
	full = append(full, one.Flush()...)

	two, _ := NewResampler(44100, 48000, 1)
	var streamed []float64
	for i := 0; i < n; i += 977 { // odd chunk size on purpose
		j := i + 977
		if j > n {
			j = n
		}
		streamed = append(streamed, two.Process(in[i:j])...)
	}
	streamed = append(streamed, two.Flush()...)
	if len(streamed) != len(full) {
		t.Fatalf("streamed %d frames vs one-shot %d", len(streamed), len(full))
	}
	for i := range full {
		if streamed[i] != full[i] {
			t.Fatalf("frame %d differs: %v vs %v", i, streamed[i], full[i])
		}
	}
}

func TestResampleDeterministicTail(t *testing.T) {
	in := make([]float64, 1000)
	for i := range in {
		in[i] = float64(i) / 1000
	}
	run := func() []float64 {
		r, _ := NewResampler(48000, 44100, 1)
		out := r.Process(in)
		return append(out, r.Flush()...)
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatal("nondeterministic length")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("nondeterministic output")
		}
	}
}

func TestChannelMatrixMonoStereo(t *testing.T) {
	m, err := ChannelMatrix(Mono, Stereo)
	if err != nil {
		t.Fatal(err)
	}
	out := ApplyMap(m, []float64{0.5, -0.25})
	if len(out) != 4 || out[0] != 0.5 || out[1] != 0.5 || out[2] != -0.25 {
		t.Fatalf("out=%v", out)
	}
	down, _ := ChannelMatrix(Stereo, Mono)
	mo := ApplyMap(down, []float64{1, -1})
	if mo[0] != 0 {
		t.Fatalf("downmix=%v", mo)
	}
	if _, err := ChannelMatrix(Mono, Layout(6)); err == nil {
		t.Fatal("unsupported layout must error")
	}
}

func TestSilenceFillRecordsMapping(t *testing.T) {
	m := SilenceFill(Mono, Layout(4))
	if len(m.Filled) != 3 {
		t.Fatalf("filled=%v", m.Filled)
	}
	out := ApplyMap(m, []float64{0.7})
	if out[0] != 0.7 || out[1] != 0 || out[2] != 0 || out[3] != 0 {
		t.Fatalf("out=%v", out)
	}
}

func TestLoudnessNormalization(t *testing.T) {
	// 1 kHz sine at -30 dBFS-ish amplitude, normalized to -23 LUFS target.
	n := 48000
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = 0.01 * math.Sin(2*math.Pi*1000*float64(i)/48000)
	}
	measured := LoudnessLUFS(samples)
	gain := -23 - measured
	ApplyGain(samples, gain)
	got := LoudnessLUFS(samples)
	if math.Abs(got-(-23)) > 0.01 {
		t.Fatalf("loudness %v, want -23", got)
	}
}

func TestTruePeakLimit(t *testing.T) {
	samples := []float64{0, 1.5, 0, -1.5, 0} // over full scale
	att := LimitTruePeak(samples, 1, -1)
	if att >= 0 {
		t.Fatalf("expected attenuation, got %v", att)
	}
	ceil := math.Pow(10, -1.0/20)
	if tp := TruePeak(samples, 1); tp > ceil+1e-9 {
		t.Fatalf("true peak %v exceeds ceiling %v", tp, ceil)
	}
	// Already-compliant signal is untouched.
	ok := []float64{0.1, -0.1}
	if att := LimitTruePeak(ok, 1, -1); att != 0 {
		t.Fatalf("unexpected attenuation %v", att)
	}
}

func TestPCMRoundTrip(t *testing.T) {
	in := []float64{0, 0.5, -0.5, 1, -1, 2, -2}
	b := FloatToS16LE(in)
	out := S16LEToFloat(b)
	for i, want := range []float64{0, 0.5, -0.5, 1, -1, 1, -1} {
		if math.Abs(out[i]-want) > 1.0/32767 {
			t.Fatalf("sample %d: got %v want %v", i, out[i], want)
		}
	}
}
