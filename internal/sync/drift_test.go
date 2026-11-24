package sync

import (
	"math"
	"testing"

	"mediasync/internal/model"
)

func ms(v int64) model.Nanos { return model.Nanos(v) * model.Nanos(model.NanosPerMilli) }

func TestEstimatorKnownDrift(t *testing.T) {
	cfg := Config{MaxPPM: 500, Window: 256, MinSamples: 8}
	e, err := NewEstimator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Candidate runs 120 ppm fast with no noise.
	for i := 0; i < 200; i++ {
		ref := model.Nanos(i) * ms(20)
		cand := model.Nanos(float64(ref) * 1.00012)
		e.Add(Pair{Reference: ref, Candidate: cand})
	}
	est := e.Estimate()
	if est.Insufficient {
		t.Fatal("unexpected insufficient estimate")
	}
	// The estimator fits reference = f(candidate), so a candidate running
	// fast is reported with a negative ppm of the same magnitude.
	if math.Abs(math.Abs(est.PPM)-120) > 1 {
		t.Errorf("ppm = %.3f, want magnitude 120", est.PPM)
	}
	if est.Clamped {
		t.Error("estimate should not clamp at 500ppm bound")
	}
}

func TestEstimatorClampsToMaxPPM(t *testing.T) {
	e, _ := NewEstimator(Config{MaxPPM: 50, Window: 256, MinSamples: 8})
	for i := 0; i < 200; i++ {
		ref := model.Nanos(i) * ms(10)
		cand := model.Nanos(float64(ref) * 1.001) // 1000 ppm, far beyond 50
		e.Add(Pair{Reference: ref, Candidate: cand})
	}
	est := e.Estimate()
	if !est.Clamped {
		t.Fatal("expected the slope to be clamped")
	}
	if math.Abs(est.PPM) > 50.0001 {
		t.Errorf("ppm = %.4f exceeds the bound", est.PPM)
	}
}

func TestEstimatorInsufficientSamples(t *testing.T) {
	e, _ := NewEstimator(Config{MaxPPM: 200, Window: 16, MinSamples: 8})
	e.Add(Pair{Reference: 0, Candidate: 0})
	est := e.Estimate()
	if !est.Insufficient {
		t.Fatal("few samples must report insufficient")
	}
	if est.Slope != 1 {
		t.Errorf("slope = %v, want neutral 1", est.Slope)
	}
}

func TestEstimatorOrderIndependent(t *testing.T) {
	pairs := make([]Pair, 50)
	for i := range pairs {
		ref := model.Nanos(i) * ms(20)
		pairs[i] = Pair{Reference: ref, Candidate: model.Nanos(float64(ref) * 1.00005)}
	}
	a, _ := NewEstimator(Config{MaxPPM: 200, Window: 64, MinSamples: 8})
	b, _ := NewEstimator(Config{MaxPPM: 200, Window: 64, MinSamples: 8})
	for _, p := range pairs {
		a.Add(p)
	}
	for i := len(pairs) - 1; i >= 0; i-- {
		b.Add(pairs[i])
	}
	if a.Estimate().PPM != b.Estimate().PPM {
		t.Errorf("order changed the estimate: %.6f vs %.6f", a.Estimate().PPM, b.Estimate().PPM)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []Config{
		{MaxPPM: 0, Window: 8, MinSamples: 4},
		{MaxPPM: 20000, Window: 8, MinSamples: 4},
		{MaxPPM: 200, Window: 1, MinSamples: 2},
		{MaxPPM: 200, Window: 8, MinSamples: 9},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("config %d should be rejected: %+v", i, c)
		}
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Errorf("default config invalid: %v", err)
	}
}

func TestCorrectorAnchorsAndPreservesStart(t *testing.T) {
	est, _ := NewEstimator(Config{MaxPPM: 500, Window: 256, MinSamples: 8})
	c := NewCorrector(est)
	for i := 0; i < 100; i++ {
		ref := ms(int64(i) * 20)
		cand := model.Nanos(float64(ref) * 1.0001)
		c.Observe(Pair{Reference: ref, Candidate: cand})
	}
	// The first observed candidate maps exactly onto the first reference.
	if got := c.Correct(0); got != 0 {
		t.Errorf("anchor not preserved: %d", got)
	}
	// A later sample is pulled onto the reference rate.
	raw := model.Nanos(10_000 * float64(ms(1)) * 1.0001)
	got := c.Correct(raw)
	want := model.Nanos(10_000 * float64(ms(1)))
	_ = want
	if diff := math.Abs(float64(got - want)); diff > 1e6 {
		t.Errorf("corrected = %d, want about %d", got, want)
	}
}

func TestApplyClockDrift(t *testing.T) {
	anchor := ms(100)
	if got := ApplyClockDrift(anchor, anchor, 1.0001); got != anchor {
		t.Errorf("anchor should map to itself, got %d", got)
	}
	// 1 s of candidate time at 1.0001 rate is 999900.1 ms of reference time.
	// 1 s / 1.0001 is 999900010 ns (999900009.999 rounded).
	if got, want := ApplyClockDrift(anchor+ms(1000), anchor, 1.0001), anchor+model.Nanos(999_900_010); got != want {
		t.Errorf("1s beyond anchor at 100ppm = %d, want %d", got, want)
	}
	if got := ApplyClockDrift(ms(5), ms(5), 1); got != ms(5) {
		t.Errorf("ratio 1 must be identity, got %d", got)
	}
}

func TestEstimateClockDriftRobustToJitter(t *testing.T) {
	rng := model.NewRNG(42)
	var ref, cand []Observation
	for i := 0; i < 500; i++ {
		arrival := model.Nanos(i) * ms(20)
		// Video media jitters around arrival; audio runs 100 ppm fast.
		ref = append(ref, Observation{Arrival: arrival, Media: arrival + model.Nanos((rng.Float()-0.5)*3e6)})
		cand = append(cand, Observation{Arrival: arrival, Media: model.Nanos(float64(arrival) * 1.0001)})
	}
	d := EstimateClockDrift(ref, cand, Config{MaxPPM: 500, Window: 256, MinSamples: 8})
	if d.Insufficient {
		t.Fatal("unexpected insufficient")
	}
	if math.Abs(d.PPM-100) > 10 {
		t.Errorf("ppm = %.2f, want about 100", d.PPM)
	}
}

func TestSkew(t *testing.T) {
	if got := Skew(ms(5), ms(3)); got != ms(2) {
		t.Errorf("Skew = %d", got)
	}
	if got := Skew(ms(1), ms(3)); got != ms(2) {
		t.Errorf("Skew must be absolute, got %d", got)
	}
}
