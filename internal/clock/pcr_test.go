package clock

import "testing"

func TestPCRUnwrapMonotonic(t *testing.T) {
	u := &PCRUnwrapper{}
	step := int64(PCRRate / 10) // 100 ms steps
	raw := int64(PCRWrapTicks - 2*step)
	v1, err := u.Unwrap(raw, false, 0)
	if err != nil || v1 != raw {
		t.Fatalf("first: v=%d err=%v", v1, err)
	}
	v2, err := u.Unwrap(raw+step, false, 188)
	if err != nil || v2 != raw+step {
		t.Fatalf("second: v=%d err=%v", v2, err)
	}
	// now wrap: raw lands near zero
	v3, err := u.Unwrap(step, false, 376)
	if err != nil {
		t.Fatalf("wrap rejected: %v", err)
	}
	want := raw + step + (PCRWrapTicks - raw) // raw+step -> 0+step across period
	if v3 != want {
		t.Fatalf("wrap value = %d, want %d", v3, want)
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps = %d", u.Wraps())
	}
	if v3 <= v2 {
		t.Fatal("unwrapped line must stay monotonic across wrap")
	}
}

func TestPCRUnwrapRejectsBackwardJump(t *testing.T) {
	u := &PCRUnwrapper{}
	v, _ := u.Unwrap(1_000_000, false, 0)
	// jump backwards while nowhere near the period boundary, no indicator
	_, err := u.Unwrap(v-100_000, false, 188)
	if e := AsError(err); e == nil || e.Code != CodePCRDiscont {
		t.Fatalf("want CodePCRDiscont, got %v", err)
	}
}

func TestPCRUnwrapResyncsOnIndicator(t *testing.T) {
	u := &PCRUnwrapper{}
	_, _ = u.Unwrap(1_000_000, false, 0)
	// discontinuity indicator permits a big jump without an error
	v, err := u.Unwrap(50_000, true, 188)
	if err != nil {
		t.Fatalf("explicit discontinuity should resync: %v", err)
	}
	if v < 1_000_000 {
		t.Fatalf("resync must not move the line backwards: %d", v)
	}
}

func TestRTPUnwrap(t *testing.T) {
	u := &RTPUnwrapper{}
	a, _ := u.Unwrap(0xFFFFFFF0, 0)
	b, err := u.Unwrap(0x00000010, 188)
	if err != nil {
		t.Fatal(err)
	}
	if b-a != 32 {
		t.Fatalf("uint32 wrap delta = %d, want 32", b-a)
	}
}

func TestDriftEstimator(t *testing.T) {
	d := &DriftEstimator{}
	// pcr = 1.001 * rtp => +1000 ppm
	for i := int64(1); i <= 5; i++ {
		rtp := i * 1_000_000
		d.Add(rtp, int64(float64(rtp)*1.001))
	}
	ppm := RoundPPM(d.PPM())
	if ppm != 1000 {
		t.Fatalf("drift ppm = %d, want 1000", ppm)
	}
	if (&DriftEstimator{}).PPM() != 0 {
		t.Fatal("empty estimator must report zero")
	}
}
