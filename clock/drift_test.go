package clock

import "testing"

func TestDriftEstimatesLinearSlope(t *testing.T) {
	// observed clock runs 120 ppm fast relative to reference
	d := NewDriftEstimator(64, 50_000_000)
	const step = 1_000_000_000
	for i := 1; i <= 32; i++ {
		x := int64(i) * step
		y := x + x*120/1_000_000
		r := d.Observe(x, y, false)
		if r.CorrectionNanos > MaxSingleCorrectionNanos || r.CorrectionNanos < -MaxSingleCorrectionNanos {
			t.Fatalf("correction %d exceeds 2 ms clamp", r.CorrectionNanos)
		}
	}
	if !d.Ready() {
		t.Fatal("estimator never became ready")
	}
	ppm := d.DriftPPM()
	if ppm < 110 || ppm > 130 {
		t.Fatalf("drift estimate %.2f ppm outside [110,130]", ppm)
	}
}

func TestDriftCorrectionClampedToTwoMillis(t *testing.T) {
	if Clamp(9_000_000) != MaxSingleCorrectionNanos {
		t.Fatal("positive clamp failed")
	}
	if Clamp(-9_000_000) != -MaxSingleCorrectionNanos {
		t.Fatal("negative clamp failed")
	}
	if Clamp(123) != 123 {
		t.Fatal("in-range clamp altered value")
	}
}

func TestDriftJumpRebuildsBaseline(t *testing.T) {
	d := NewDriftEstimator(32, 10_000_000)
	for i := 1; i <= 8; i++ {
		x := int64(i) * 1_000_000
		d.Observe(x, x, false)
	}
	r := d.Observe(9_000_000, 500_000_000, false)
	if r.Reason != RecoveryJump {
		t.Fatalf("expected jump, got %s", r.Reason)
	}
	if d.Resets() != 1 || d.Jumps() != 1 {
		t.Fatalf("resets=%d jumps=%d", d.Resets(), d.Jumps())
	}
	// model restarts cleanly and re-converges
	for i := 1; i <= 8; i++ {
		x := 500_000_000 + int64(i)*1_000_000
		r2 := d.Observe(x, x, false)
		if r2.Reason == RecoveryJump {
			t.Fatal("post-jump observations should be stable")
		}
	}
}

func TestDriftExplicitResetRecorded(t *testing.T) {
	d := NewDriftEstimator(16, 10_000_000)
	d.Observe(0, 0, false)
	d.Observe(1_000_000, 1_000_000, false)
	r := d.Observe(2_000_000, 1_000, true)
	if r.Reason != RecoveryClockReset {
		t.Fatalf("got %s", r.Reason)
	}
	if d.Resets() != 1 {
		t.Fatalf("resets=%d", d.Resets())
	}
}

func TestDriftCheckpointDeterministic(t *testing.T) {
	d := NewDriftEstimator(16, 50_000_000)
	for i := 1; i <= 10; i++ {
		x := int64(i) * 1_000_000
		d.Observe(x, x+int64(i)*120, false)
	}
	snap := d.Snapshot()
	d2 := RestoreDrift(snap)
	if d2.DriftPPM() != d.DriftPPM() {
		t.Fatalf("ppm mismatch after restore: %v vs %v", d2.DriftPPM(), d.DriftPPM())
	}
	for i := 11; i <= 16; i++ {
		x := int64(i) * 1_000_000
		a := d.Observe(x, x+int64(i)*120, false)
		b := d2.Observe(x, x+int64(i)*120, false)
		if a != b {
			t.Fatalf("restored estimator diverges at %d", i)
		}
	}
}
