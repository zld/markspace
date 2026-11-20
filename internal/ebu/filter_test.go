package ebu

import (
	"math"
	"math/cmplx"
	"testing"
)

func response(q Biquad, fs, f float64) complex128 {
	z := cmplx.Exp(complex(0, -2*math.Pi*f/fs))
	return (complex(q.B0, 0) + complex(q.B1, 0)*z + complex(q.B2, 0)*z*z) /
		(1 + complex(q.A1, 0)*z + complex(q.A2, 0)*z*z)
}

func TestCoefficientsAt48kMatchReference(t *testing.T) {
	kw, err := KWeightingFor(48000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reference values from the De Man derivation used by libebur128. The
	// 48 kHz pair is pre-seeded, so this also pins the table's first entry.
	wantHP := []float64{0.9950299263000474, -1.9900598526000948, 0.9950299263000474}
	wantHPA := []float64{-1.9900474548339797, 0.9900722503662099}
	gotHP := []float64{kw.HighPass.B0, kw.HighPass.B1, kw.HighPass.B2}
	gotHPA := []float64{kw.HighPass.A1, kw.HighPass.A2}
	for i := range wantHP {
		if math.Abs(gotHP[i]-wantHP[i]) > 1e-12 {
			t.Errorf("high-pass b%d = %.15f, want %.15f", i, gotHP[i], wantHP[i])
		}
	}
	for i := range wantHPA {
		if math.Abs(gotHPA[i]-wantHPA[i]) > 1e-12 {
			t.Errorf("high-pass a%d = %.15f, want %.15f", i+1, gotHPA[i], wantHPA[i])
		}
	}
	wantShelf := []float64{1.5351248595869702, -2.6916961894063807, 1.19839281085285}
	gotShelf := []float64{kw.Shelf.B0, kw.Shelf.B1, kw.Shelf.B2}
	for i := range wantShelf {
		if math.Abs(gotShelf[i]-wantShelf[i]) > 1e-12 {
			t.Errorf("shelf b%d = %.15f, want %.15f", i, gotShelf[i], wantShelf[i])
		}
	}
}

func TestKWeightingCurve(t *testing.T) {
	kw, _ := KWeightingFor(48000)
	cases := []struct {
		freq float64
		want float64
	}{
		{20, -13.32},
		{100, -1.18},
		{1000, 0.65},
		{2000, 3.03},
		{10000, 4.00},
	}
	for _, c := range cases {
		h := response(kw.HighPass, 48000, c.freq) * response(kw.Shelf, 48000, c.freq)
		db := 20 * math.Log10(cmplx.Abs(h))
		if math.Abs(db-c.want) > 0.05 {
			t.Errorf("%.0f Hz: %.3f dB, want %.3f dB", c.freq, db, c.want)
		}
	}
}

func TestChainStateIsPerChannel(t *testing.T) {
	kw, _ := KWeightingFor(48000)
	a := NewChain(kw)
	b := NewChain(kw)
	// Feed identical impulse to both, then a different signal to b: states
	// must not leak between chains.
	for i := 0; i < 100; i++ {
		var x float64
		if i == 0 {
			x = 1
		}
		if a.Process(x) != b.Process(x) {
			t.Fatalf("chains diverged at sample %d", i)
		}
	}
	b.Process(1)
	if a.Process(0) == 0 && b.Process(0) == 0 {
		t.Fatal("expected non-zero filter tail")
	}
}

func TestArbitraryRatesAreDerived(t *testing.T) {
	// Rates outside the common set are derived at their real rate rather than
	// rejected: a 32 kHz asset must measure correctly, not be refused.
	for _, rate := range []int{8000, 22050, 32000, 64000} {
		kw, err := KWeightingFor(rate)
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		if kw.Rate != rate {
			t.Errorf("%d Hz: rate = %d", rate, kw.Rate)
		}
		// The design matches the 48 kHz reference closely while the shelf is
		// in its flat region and warping stays small; at 8 kHz the bilinear
		// transform pushes the shelf down by about 0.2 dB at 1 kHz, which is
		// inherent to deriving at that rate rather than borrowing 48 kHz
		// coefficients.
		h := response(kw.HighPass, float64(rate), 1000) * response(kw.Shelf, float64(rate), 1000)
		got := 20 * math.Log10(cmplx.Abs(h))
		tolerance := 0.15
		if rate < 16000 {
			tolerance = 0.3
		}
		if math.Abs(got-0.65) > tolerance {
			t.Errorf("%d Hz: 1 kHz gain = %.3f dB", rate, got)
		}
	}
	if _, err := KWeightingFor(7999); err == nil {
		t.Fatal("expected error below the minimum rate")
	}
}

func TestCoefficientsAreCachedPerRate(t *testing.T) {
	a, _ := KWeightingFor(32000)
	b, _ := KWeightingFor(32000)
	if a.HighPass != b.HighPass || a.Shelf != b.Shelf {
		t.Fatal("cached coefficients differ between calls")
	}
}
