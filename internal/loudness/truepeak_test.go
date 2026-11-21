package loudness

import (
	"math"
	"testing"

	"loudcheck/internal/dsp"
	"loudcheck/internal/fixture"
)

// Two analyzers must not share filter state: measuring silence after a loud
// tone on a fresh analyzer must stay silent.
func TestAnalyzerIsolation(t *testing.T) {
	loud := fixture.Signal(fixture.Options{
		SampleRate: testRate, Channels: 1, Seconds: 3,
		Encoding: fixture.Float32, ToneDBFS: -6, ToneHz: 997,
	})
	a := NewAnalyzer(testRate, []float64{1})
	buf := make([]float32, len(loud))
	for i, v := range loud {
		buf[i] = float32(v)
	}
	a.Write(buf)
	a.Finish()

	b := NewAnalyzer(testRate, []float64{1})
	silence := make([]float32, testRate)
	b.Write(silence)
	res := b.Finish()
	if res.IntegratedValid {
		t.Fatalf("fresh analyzer measured silence as %.2f LUFS", res.IntegratedLUFS)
	}
	if res.TruePeak != 0 {
		t.Fatalf("fresh analyzer reports a true peak of %v from leaked state", res.TruePeak)
	}
}

func TestTruePeakExceedsSamplePeak(t *testing.T) {
	// A sine at 1/4 of the sample rate with a 45 degree start is sampled at
	// +-0.707 of its analog amplitude and never hits the crest, so the
	// sample peak understates the true peak by exactly 3.01 dB.
	opts := fixture.Options{
		SampleRate: testRate, Channels: 1, Seconds: 4,
		Encoding: fixture.Float32, ToneDBFS: -6, ToneHz: testRate / 4,
		Phase: math.Pi / 4,
	}
	sig := fixture.Signal(opts)
	tp := dsp.NewTruePeak()
	tp.Write(sig)
	tp.Finish()

	// At fs/4 with a 45 degree start, consecutive samples are sin(45 + 90k):
	// they alternate between +0.707 and -0.707 of the analog amplitude.
	wantSample := 0.5 * math.Cos(math.Pi/4)
	if !near(tp.SamplePeak(), wantSample, 2e-3) {
		t.Errorf("sample peak %v, want %v", tp.SamplePeak(), wantSample)
	}
	wantTP := 0.5 // the analog sine peaks at full amplitude
	gotDB := 20 * math.Log10(tp.Peak())
	wantDB := 20 * math.Log10(wantTP)
	if !near(gotDB, wantDB, 0.1) {
		t.Errorf("true peak %.3f dB, want %.3f dB", gotDB, wantDB)
	}
	if tp.Peak() <= tp.SamplePeak() {
		t.Errorf("true peak %v should exceed sample peak %v", tp.Peak(), tp.SamplePeak())
	}
}

func TestTruePeakAtFileEdges(t *testing.T) {
	// A short burst at the very start and end must be seen by the boundary
	// handling; a detector that only looks at interior samples would miss it.
	sig := make([]float64, 1000)
	for i := 0; i < 40; i++ {
		sig[i] = 0.9 * math.Sin(2*math.Pi*float64(i)/8)
		sig[len(sig)-1-i] = sig[i]
	}
	tp := dsp.NewTruePeak()
	for i := 0; i < len(sig); i += 17 {
		end := i + 17
		if end > len(sig) {
			end = len(sig)
		}
		tp.Write(sig[i:end])
	}
	tp.Finish()
	if tp.Peak() < 0.85 {
		t.Errorf("edge burst peak %v, want at least 0.85", tp.Peak())
	}
}

func TestTruePeakOfConstantIsExact(t *testing.T) {
	sig := make([]float64, 500)
	for i := range sig {
		sig[i] = 0.5
	}
	tp := dsp.NewTruePeak()
	tp.Write(sig)
	tp.Finish()
	// Neither edge padding nor interpolation may invent a peak above the DC
	// level; an interpolator fed zero outside the file would ring here.
	if !near(tp.Peak(), 0.5, 1e-9) {
		t.Errorf("DC true peak = %v, want 0.5", tp.Peak())
	}
}

// A loud sample at the very first or last position must still be reported:
// the edges are covered by the raw samples even though interpolation skips
// the kernel margin.
func TestTruePeakCoversFirstAndLastSample(t *testing.T) {
	const n = 256
	first := make([]float64, n)
	last := make([]float64, n)
	first[0] = 0.99
	last[n-1] = 0.99

	for name, sig := range map[string][]float64{"first": first, "last": last} {
		tp := dsp.NewTruePeak()
		tp.Write(sig)
		tp.Finish()
		if !near(tp.Peak(), 0.99, 1e-9) {
			t.Errorf("%s-sample peak = %v, want 0.99", name, tp.Peak())
		}
		if !near(tp.SamplePeak(), 0.99, 1e-9) {
			t.Errorf("%s raw sample peak = %v, want 0.99", name, tp.SamplePeak())
		}
	}
}

func TestDBHandlesZero(t *testing.T) {
	if !math.IsInf(DB(0), -1) {
		t.Fatal("DB(0) should be -Inf, not a fabricated floor")
	}
	if !near(DB(0.5), -6.0206, 1e-3) {
		t.Errorf("DB(0.5) = %v", DB(0.5))
	}
}

func TestLayoutResolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels int
		mapping  string
		want     string
		wantErr  bool
	}{
		{name: "", channels: 1, want: "mono"},
		{name: "", channels: 2, want: "stereo"},
		{name: "5.1", channels: 6, want: "5.1"},
		{name: "7.1", channels: 8, want: "7.1"},
		{name: "", channels: 6, want: "5.1"},
		{name: "", channels: 4, wantErr: true},
		{name: "", channels: 4, mapping: "1,1,1.41,1.41", want: "custom"},
		{name: "", channels: 4, mapping: "1,1,1", wantErr: true},
		{name: "stereo", channels: 6, wantErr: true},
		{name: "nonsense", channels: 2, wantErr: true},
		{name: "5.1", channels: 6, mapping: "1,1,1,0,1,1", wantErr: true},
	} {
		l, err := Resolve(tc.name, tc.channels, tc.mapping)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%+v: expected an error", tc)
			}
			continue
		}
		if err != nil {
			t.Errorf("%+v: %v", tc, err)
			continue
		}
		if l.Name != tc.want {
			t.Errorf("%+v: layout = %q", tc, l.Name)
		}
		if len(l.Weights) != tc.channels {
			t.Errorf("%+v: %d weights for %d channels", tc, len(l.Weights), tc.channels)
		}
	}
}

func TestLFEWeightIsZero(t *testing.T) {
	for _, l := range []Layout{Layout51, Layout71} {
		if l.Weights[3] != 0 {
			t.Errorf("%s LFE weight = %v, want 0", l.Name, l.Weights[3])
		}
		for i := 4; i < l.Channels; i++ {
			if l.Weights[i] != surroundWeight {
				t.Errorf("%s channel %d weight = %v", l.Name, i, l.Weights[i])
			}
		}
	}
}
