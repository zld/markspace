package dsp

import (
	"math"
	"testing"
)

// The BS.1770-4 reference coefficients for 48 kHz are published to six
// decimal places; deriving them from the analog prototypes must reproduce
// them to within that precision.
func TestKFilterMatchesBS1770At48k(t *testing.T) {
	f := NewKFilter(48000)
	cases := []struct {
		name string
		got  Biquad
		want Biquad
	}{
		{"shelf", f.Stage1, Biquad{
			B0: 1.53512485958697, B1: -2.69169618940638, B2: 1.19839281085285,
			A1: -1.69065929318241, A2: 0.73248077421585,
		}},
		{"highpass", f.Stage2, Biquad{
			B0: 1, B1: -2, B2: 1,
			A1: -1.99004745483398, A2: 0.99007225036621,
		}},
	}
	for _, c := range cases {
		for label, pair := range map[string][2]float64{
			"B0": {c.got.B0, c.want.B0}, "B1": {c.got.B1, c.want.B1},
			"B2": {c.got.B2, c.want.B2}, "A1": {c.got.A1, c.want.A1},
			"A2": {c.got.A2, c.want.A2},
		} {
			if math.Abs(pair[0]-pair[1]) > 1e-6 {
				t.Errorf("%s %s = %.12f, want %.12f", c.name, label, pair[0], pair[1])
			}
		}
	}
}

// The K curve applies roughly +0.7 dB at 997 Hz and rolls off low
// frequencies; a filter that ignored the sample rate would show a large
// error at 44.1 or 96 kHz instead.
func TestKFilterResponseAt997Hz(t *testing.T) {
	for _, fs := range []int{44100, 48000, 88200, 96000} {
		f := NewKFilter(fs)
		gain := filterGainDB(f, 997, fs)
		if math.Abs(gain-0.69) > 0.12 {
			t.Errorf("%d Hz: gain at 997 Hz = %.3f dB, want about 0.69", fs, gain)
		}
		if low := filterGainDB(f, 40, fs); low > -3 {
			t.Errorf("%d Hz: gain at 40 Hz = %.3f dB, want a low-frequency rolloff", fs, low)
		}
	}
}

// filterGainDB measures the response by feeding a long sine and comparing the
// steady-state output amplitude with the input amplitude.
func filterGainDB(f *Filter, freq float64, fs int) float64 {
	f.Reset()
	const n = 48000
	amp := 0.1
	var in, out float64
	// Skip the first half so the transient does not bias the estimate.
	for i := 0; i < n; i++ {
		x := amp * math.Sin(2*math.Pi*freq*float64(i)/float64(fs))
		y := f.Process(x)
		if i >= n/2 {
			in = math.Max(in, math.Abs(x))
			out = math.Max(out, math.Abs(y))
		}
	}
	if in == 0 {
		return -999
	}
	return 20 * math.Log10(out/in)
}

func TestKFilterResetClearsState(t *testing.T) {
	f := NewKFilter(48000)
	for i := 0; i < 1000; i++ {
		f.Process(0.5)
	}
	f.Reset()
	// After a reset on silence, output must be exactly zero: any residue
	// would leak one file's tail into the next measurement.
	for i := 0; i < 10; i++ {
		if y := f.Process(0); y != 0 {
			t.Fatalf("sample %d after reset = %v, want 0", i, y)
		}
	}
}

func TestKFilterIsFiniteOnPathologicalInput(t *testing.T) {
	f := NewKFilter(48000)
	seq := []float64{1, -1, 1, -1, 0.999, -0.999}
	for r := 0; r < 2000; r++ {
		for _, x := range seq {
			if y := f.Process(x); math.IsNaN(y) || math.IsInf(y, 0) {
				t.Fatalf("filter produced %v for input %v", y, x)
			}
		}
	}
}
