package align

import (
	"math"
	"testing"

	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
)

func TestEstimateDriftExactSlope(t *testing.T) {
	// Media clock runs 200 ppm fast relative to reference.
	var pts []Point
	for i := 0; i < 10; i++ {
		ref := float64(i) * 10
		media := ref / 1.0002
		pts = append(pts, Point{Media: media, Ref: ref})
	}
	d, err := EstimateDrift(pts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(d.PPM-200) > 0.5 {
		t.Fatalf("ppm = %.3f, want 200", d.PPM)
	}
	if d.RMSEms > 1e-6 || d.MaxResidMs > 1e-6 {
		t.Fatalf("residuals = %v / %v", d.RMSEms, d.MaxResidMs)
	}
}

func TestEstimateDriftNegativeAndOffset(t *testing.T) {
	// Media clock slow by 500 ppm plus a 100 ms capture offset.
	var pts []Point
	for i := 0; i < 8; i++ {
		ref := 100 + float64(i)*5
		media := (ref - 100) / (1 - 500e-6)
		pts = append(pts, Point{Media: media, Ref: ref})
	}
	d, err := EstimateDrift(pts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(d.PPM+500) > 0.5 {
		t.Fatalf("ppm = %.3f", d.PPM)
	}
	if math.Abs(d.Correct(0)-100) > 1e-3 {
		t.Fatalf("intercept correction = %v", d.Correct(0))
	}
}

func TestEstimateDriftNeedsBase(t *testing.T) {
	_, err := EstimateDrift([]Point{{Media: 1, Ref: 1}, {Media: 2, Ref: 2}}, 17)
	f, ok := diag.As(err)
	if !ok || f.Code != diag.CodeMissingBase || f.Offset != 17 {
		t.Fatalf("err = %v", err)
	}
	// Duplicate media times carry no slope.
	_, err = EstimateDrift([]Point{{Media: 1, Ref: 1}, {Media: 1, Ref: 2}, {Media: 1, Ref: 3}}, 0)
	if !diag.IsCode(err, diag.CodeMissingBase) {
		t.Fatalf("duplicate media accepted: %v", err)
	}
}

func TestDeriveIntervalsUsesRealSampleTime(t *testing.T) {
	// 23.976 then a 100 ms hold frame (true VFR), never averaged away.
	dts := []int64{0, 3754, 7508, 11262, 11262 + 9000, 11262 + 9000 + 3754}
	ivs, faults, err := DeriveIntervals(dts, 0, VFROptions{Timescale: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if len(ivs) != 5 {
		t.Fatalf("intervals = %d", len(ivs))
	}
	if math.Abs(ivs[0].DurationMs-41.711) > 0.01 {
		t.Fatalf("first interval = %v", ivs[0].DurationMs)
	}
	if !ivs[3].Anomalous {
		t.Fatalf("hold frame not flagged: %+v", ivs)
	}
	if len(faults) != 1 || faults[0].Code != diag.CodeVFRGap {
		t.Fatalf("faults = %v", faults)
	}
	median := MedianFPS(ivs)
	mean := MeanFPS(ivs)
	if median < 23.9 || median > 24.0 {
		t.Fatalf("median fps = %v", median)
	}
	if mean >= median {
		t.Fatalf("mean %v should be below median %v for a hold frame", mean, median)
	}
}

func TestDeriveIntervalsRollbackHasOffset(t *testing.T) {
	dts := []int64{0, 3000, 6000, 5000, 9000}
	_, faults, err := DeriveIntervals(dts, 0, VFROptions{Timescale: 90000})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := diag.As(faults[0])
	if !ok || f.Code != diag.CodeTimestampRollback || f.Offset != 3 {
		t.Fatalf("rollback = %+v", faults)
	}
	if !f.HasMedia {
		t.Fatal("rollback fault lacks media position")
	}
}

func TestDeriveIntervalsMissingBase(t *testing.T) {
	if _, _, err := DeriveIntervals([]int64{0, 1}, 0, VFROptions{}); !diag.IsCode(err, diag.CodeMissingBase) {
		t.Fatalf("missing timescale accepted: %v", err)
	}
	if _, _, err := DeriveIntervals([]int64{0}, 0, VFROptions{Timescale: 90000}); !diag.IsCode(err, diag.CodeMissingBase) {
		t.Fatalf("one sample accepted: %v", err)
	}
}

func TestRationalCorrectionIsStable(t *testing.T) {
	d := DriftResult{PPM: 1000, Intercept: 0.5, Stable: true}
	// 1 s media on a 1 kHz clock at +1000 ppm is 1.001 s + 0.5 s offset.
	got := d.CorrectRational(clock.NewRat(1000, 1000), 1000)
	if got.String() != "1501/1000" {
		t.Fatalf("corrected=%v want 1501/1000", got)
	}
}
