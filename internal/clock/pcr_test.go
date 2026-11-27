package clock

import (
	"errors"
	"testing"

	"tsrepair/internal/mediaerr"
)

func TestPCRUnwrapMonotonic(t *testing.T) {
	var u PCRUnwrapper
	// Counter climbs monotonically, wraps past PCRMod, then keeps climbing.
	seq := []int64{100, 200, PCRMod - 50, 10, 20}
	want := []int64{100, 200, PCRMod - 50, PCRMod + 10, PCRMod + 20}
	wraps := 0
	for i, raw := range seq {
		got, wrapped, err := u.Unwrap(raw)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got != want[i] {
			t.Errorf("Unwrap(%d)=%d want %d", raw, got, want[i])
		}
		if wrapped {
			wraps++
		}
	}
	if wraps != 1 {
		t.Errorf("wrap events=%d want 1", wraps)
	}
}

func TestPCRUnwrapDoubleWrap(t *testing.T) {
	var u PCRUnwrapper
	steps := []struct {
		raw  int64
		want int64
	}{
		{PCRMod - 10, PCRMod - 10},
		{5, PCRMod + 5},
		{PCRMod - 20, 2*PCRMod - 20}, // climbs back up to the period boundary
		{7, 2*PCRMod + 7},             // second forward wrap
	}
	for _, s := range steps {
		got, _, err := u.Unwrap(s.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != s.want {
			t.Errorf("got %d want %d", got, s.want)
		}
	}
}

func TestPCRUnwrapBackwardDiscontinuity(t *testing.T) {
	var u PCRUnwrapper
	// Establish one forward wrap, then make a small genuine backward jump
	// (not large enough to be a second wrap).
	if _, _, err := u.Unwrap(PCRMod - 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.Unwrap(20); err != nil {
		t.Fatal(err)
	}
	ticks, wrapped, err := u.Unwrap(15)
	if err == nil {
		t.Fatal("expected discontinuity error")
	}
	var me *mediaerr.Error
	if !errors.As(err, &me) || me.Code != mediaerr.CodePCRDiscontinuity {
		t.Fatalf("want CodePCRDiscontinuity, got %v", err)
	}
	if wrapped {
		t.Error("discontinuity must not be reported as a wrap")
	}
	// Wrap budget stays at one period: value is raw + PCRMod.
	if ticks != PCRMod+15 {
		t.Errorf("ticks=%d", ticks)
	}
}

func TestPCRUnwrapRejectsOutOfRange(t *testing.T) {
	var u PCRUnwrapper
	for _, bad := range []int64{-1, PCRMod, PCRMod + 1} {
		if _, _, err := u.Unwrap(bad); err == nil {
			t.Errorf("expected error for raw=%d", bad)
		}
	}
}

func TestDriftEstimatorClampsAndIsolates(t *testing.T) {
	ec := &mediaerr.Collector{}
	d := &DriftEstimator{MaxCorrectionTicks: 100, OutlierTicks: 5000}

	obs, err := d.Observe(0, 0, ec, 0x100, 0)
	if err != nil || obs.CorrectedTicks != 0 {
		t.Fatalf("anchor obs=%+v err=%v", obs, err)
	}

	// Desired correction is 1300 ticks but the single-step cap is 100.
	obs, err = d.Observe(1000, 2300, ec, 0x100, 188)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Clamped || obs.Correction != 100 || d.TotalCorrection != 100 {
		t.Errorf("expected clamp to 100, got %+v total=%d", obs, d.TotalCorrection)
	}
	if ec.CountByCode(mediaerr.CodePCRDriftClamped) != 1 {
		t.Error("clamp event not recorded")
	}

	// Outlier: deviation > 5000 between adjacent observations.
	before := d.TotalCorrection
	obs, err = d.Observe(2000, 20_000, ec, 0x100, 376)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Outlier {
		t.Error("expected outlier isolation")
	}
	if d.TotalCorrection != before {
		t.Error("outlier must not change estimator state")
	}
	if ec.CountByCode(mediaerr.CodePCROutlier) != 1 {
		t.Error("outlier event not recorded")
	}
}

func TestDriftEstimatorTracksSmallDrift(t *testing.T) {
	ec := &mediaerr.Collector{}
	d := &DriftEstimator{MaxCorrectionTicks: 10_000, OutlierTicks: 1_000_000}
	// Local line runs 10 ticks ahead per 1000 sender ticks; anchor at origin.
	if _, err := d.Observe(0, 0, ec, 0, 0); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		pcr := int64(i) * 1000
		local := int64(i)*1000 + int64(i)*10
		obs, err := d.Observe(pcr, local, ec, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Clamped || obs.Outlier {
			t.Fatalf("step %d unexpected flag: %+v", i, obs)
		}
	}
	if d.TotalCorrection != 50 {
		t.Errorf("total correction=%d want 50", d.TotalCorrection)
	}
}
