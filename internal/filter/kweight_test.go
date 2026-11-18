package filter

import (
	"math"
	"testing"
)

func almostEq(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestCoefficients48k(t *testing.T) {
	k, err := NewKWeight(48000)
	if err != nil {
		t.Fatal(err)
	}
	// Published BS.1770 reference coefficients at 48 kHz.
	shelf := Biquad{B0: 1.53512485958697, B1: -2.69169618940638, B2: 1.19839281085285,
		A1: -1.69065929318241, A2: 0.73248077421585}
	hp := Biquad{B0: 1, B1: -2, B2: 1, A1: -1.99004745483398, A2: 0.99007225036621}
	const tol = 1e-6
	for name, got := range map[string][5]float64{
		"shelf.b0": {k.shelf.B0, shelf.B0}, "shelf.b1": {k.shelf.B1, shelf.B1},
		"shelf.b2": {k.shelf.B2, shelf.B2}, "shelf.a1": {k.shelf.A1, shelf.A1},
		"shelf.a2": {k.shelf.A2, shelf.A2},
		"hp.b1":    {k.hp.B1, hp.B1}, "hp.a1": {k.hp.A1, hp.A1}, "hp.a2": {k.hp.A2, hp.A2},
	} {
		if !almostEq(got[0], got[1], tol) {
			t.Errorf("%s = %.12f, want %.12f", name, got[0], got[1])
		}
	}
}

func TestSupportedRatesStable(t *testing.T) {
	for _, rate := range []int{8000, 11025, 22050, 44100, 48000, 96000, 192000, 384000} {
		k, err := NewKWeight(rate)
		if err != nil {
			t.Errorf("%d Hz: %v", rate, err)
			continue
		}
		if !k.shelf.Stable() || !k.hp.Stable() {
			t.Errorf("%d Hz: unstable section", rate)
		}
	}
}

func TestUnsupportedRates(t *testing.T) {
	for _, rate := range []int{0, -1, 7999, 384001} {
		if _, err := NewKWeight(rate); err == nil {
			t.Errorf("%d Hz: expected error", rate)
		}
	}
}

func TestHighPassRejectsDC(t *testing.T) {
	k, _ := NewKWeight(48000)
	var y float64
	for i := 0; i < 48000*5; i++ {
		y = k.Process(1.0)
	}
	if math.Abs(y) > 1e-3 {
		t.Errorf("DC not rejected: steady-state output %v", y)
	}
}

// chainGain evaluates the magnitude response of the two sections at
// angular frequency w (radians/sample).
func chainGain(k *KWeight, w float64) float64 {
	mag := func(b Biquad) float64 {
		re1, im1 := math.Cos(-w), math.Sin(-w)
		re2, im2 := math.Cos(-2*w), math.Sin(-2*w)
		nre := b.B0 + b.B1*re1 + b.B2*re2
		nim := b.B1*im1 + b.B2*im2
		dre := 1 + b.A1*re1 + b.A2*re2
		dim := b.A1*im1 + b.A2*im2
		return math.Hypot(nre, nim) / math.Hypot(dre, dim)
	}
	return mag(k.shelf) * mag(k.hp)
}

func TestSineGainMatchesTheory(t *testing.T) {
	const rate = 48000
	k, _ := NewKWeight(rate)
	freq := 997.0
	n := rate * 3
	var sumIn, sumOut float64
	for i := 0; i < n; i++ {
		x := math.Sin(2 * math.Pi * freq * float64(i) / rate)
		y := k.Process(x)
		if i >= rate { // skip filter transient
			sumIn += x * x
			sumOut += y * y
		}
	}
	measured := math.Sqrt(sumOut / sumIn)
	want := chainGain(k, 2*math.Pi*freq/rate)
	if math.Abs(measured-want)/want > 0.01 {
		t.Errorf("gain at %v Hz = %.5f, want %.5f", freq, measured, want)
	}
	// K-weighting near 1 kHz should be close to unity.
	if want < 0.9 || want > 1.2 {
		t.Errorf("unexpected K-weight gain %.5f at %v Hz", want, freq)
	}
}

func TestStatePersistsAcrossBlocks(t *testing.T) {
	k1, _ := NewKWeight(48000)
	k2, _ := NewKWeight(48000)
	// k1 processes one big block; k2 processes the same samples split up.
	full := make([]float64, 1000)
	for i := range full {
		full[i] = math.Sin(2 * math.Pi * 440 * float64(i) / 48000)
	}
	var out1, out2 []float64
	for _, x := range full {
		out1 = append(out1, k1.Process(x))
	}
	for _, chunk := range [][2]int{{0, 333}, {333, 777}, {777, 1000}} {
		for _, x := range full[chunk[0]:chunk[1]] {
			out2 = append(out2, k2.Process(x))
		}
	}
	for i := range out1 {
		if out1[i] != out2[i] {
			t.Fatalf("sample %d differs: %v vs %v", i, out1[i], out2[i])
		}
	}
}
