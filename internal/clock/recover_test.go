package clock

import "testing"

func TestDriftEstimatorSyntheticPPM(t *testing.T) {
	const rate uint32 = 90000
	const ppm = 250.0
	// Device emits RTP = nominal * (1+ppm) as wall time advances.
	devicePerNS := (float64(rate) / 1e9) * (1 + ppm/1e6)
	est := NewDriftEstimator()
	for i := 1; i <= 20; i++ {
		wall := int64(i) * 100_000_000 // 100 ms steps
		rtp := int64(devicePerNS * float64(wall))
		if err := est.Add(SRPoint{RTP: rtp, WallNS: wall, Arrival: wall}); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := est.PPM(rate)
	if !ok {
		t.Fatal("no estimate produced")
	}
	if d := got - ppm; d > 1 || d < -1 {
		t.Fatalf("drift estimate %.3f ppm, want %.1f ±1", got, ppm)
	}

	mc := NewMediaClock(rate)
	if !mc.Fit(est) {
		t.Fatal("media clock fit failed")
	}
	mc.Anchor(0)
	// 1e9 ns of media should map back to ~1e9 ns regardless of drift.
	rtpAtOneSec := int64(devicePerNS * 1e9)
	ns, err := mc.ToMediaNS(rtpAtOneSec)
	if err != nil {
		t.Fatal(err)
	}
	// Half an integer RTP tick is ~5.6us of quantization; tolerate 10us.
	if d := ns - 1_000_000_000; d > 10000 || d < -10000 {
		t.Fatalf("corrected media time off by %d ns", d)
	}
}

func TestDriftEstimatorNeedsTwoPoints(t *testing.T) {
	est := NewDriftEstimator()
	if _, ok := est.PPM(90000); ok {
		t.Fatal("estimate available without points")
	}
	if err := est.Add(SRPoint{RTP: 100, WallNS: 10}); err != nil {
		t.Fatal(err)
	}
	if err := est.Add(SRPoint{RTP: 50, WallNS: 20}); err == nil {
		t.Fatal("non-monotonic report accepted")
	}
}

func TestMediaClockMonotonicAndBounds(t *testing.T) {
	mc := NewMediaClock(90000)
	if _, err := mc.ToMediaNS(123); err == nil {
		t.Fatal("unanchored clock produced a time")
	}
	mc.Anchor(9000)
	a, err := mc.ToMediaNS(9000)
	if err != nil || a != 0 {
		t.Fatalf("anchor mapping = %d, %v", a, err)
	}
	b, err := mc.ToMediaNS(9000 * 2)
	if err != nil || b <= a {
		t.Fatalf("non-monotonic mapping: %d then %d", a, b)
	}
}
