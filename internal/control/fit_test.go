package control

import (
	"errors"
	"math"
	"testing"
)

func TestFitLineExact(t *testing.T) {
	// y = 10 - 2x
	xs := []float64{0, 1, 2, 3}
	ys := []float64{10, 8, 6, 4}
	m, err := FitLine(xs, ys)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Slope+2) > 1e-9 || math.Abs(m.Intercept-10) > 1e-9 {
		t.Fatalf("unexpected model: %+v", m)
	}
	if got := m.Predict(4); math.Abs(got-2) > 1e-9 {
		t.Fatalf("predict: %v", got)
	}
}

func TestFitLineDegenerate(t *testing.T) {
	if _, err := FitLine([]float64{1, 1}, []float64{2, 3}); err == nil {
		t.Fatal("constant x should fail QR solve")
	}
	if _, err := FitLine([]float64{1}, []float64{2}); err == nil {
		t.Fatal("single sample should be rejected")
	}
}

func TestFitBitrateAndInversion(t *testing.T) {
	// Exponential decay: b = 1000 * exp(-0.15*qp)
	samples := []Sample{
		{QP: 5, BitrateKbps: 1000 * math.Exp(-0.15*5)},
		{QP: 10, BitrateKbps: 1000 * math.Exp(-0.15*10)},
		{QP: 20, BitrateKbps: 1000 * math.Exp(-0.15*20)},
	}
	m, err := FitBitrate(samples)
	if err != nil {
		t.Fatalf("fit: %v", err)
	}
	if math.Abs(m.Slope+0.15) > 1e-9 {
		t.Fatalf("slope: %v", m.Slope)
	}
	// Predicting the bitrate of QP=12 should round-trip closely.
	got := PredictBitrate(m, 1000*math.Exp(-0.15*12))
	if got != 12 {
		t.Fatalf("predict QP: got %d", got)
	}

	// Inverted relation: bitrate grows with QP.
	bad := []Sample{
		{QP: 5, BitrateKbps: 100},
		{QP: 10, BitrateKbps: 200},
		{QP: 20, BitrateKbps: 400},
	}
	if _, err := FitBitrate(bad); !errors.Is(err, ErrInverted) {
		t.Fatalf("want ErrInverted, got %v", err)
	}
}

func TestFitQuality(t *testing.T) {
	s := []Sample{
		{QP: 2, PSNRdB: 44}, {QP: 12, PSNRdB: 34}, {QP: 22, PSNRdB: 24},
	}
	m, err := FitQuality(s)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Slope+1) > 1e-9 || math.Abs(m.Intercept-46) > 1e-9 {
		t.Fatalf("quality model: %+v", m)
	}
}
