package clock

import (
	"math"
	"testing"

	"mediad/internal/media"
)

func TestCalibrateAndDriftCorrection(t *testing.T) {
	const blockNs = int64(40e6)
	// Audio track runs 3000 ppm fast, plus a 2 s start offset.
	tl := New(1, Options{MinSamples: 4})
	var vRaw, aRaw []int64
	for _, o := range genFeed(2000, 1000, blockNs, 0) {
		vRaw = append(vRaw, tl.Observe(o))
	}
	for _, o := range genFeed(2000, 3000, blockNs, 3000) {
		o.Track = 2
		aRaw = append(aRaw, tl.Observe(o))
	}
	if err := tl.Calibrate(); err != nil {
		t.Fatal(err)
	}
	refDrift, _ := tl.DriftPPM(1)
	audDrift, _ := tl.DriftPPM(2)
	if math.Abs(refDrift) > 50 {
		t.Fatalf("reference drift should be ~0, got %.1f ppm", refDrift)
	}
	if math.Abs(audDrift-3000) > 50 {
		t.Fatalf("audio drift estimate off: %.1f ppm", audDrift)
	}

	// Replay both tracks through Corrected: the drift must cancel so the two
	// tracks never diverge by more than one block after 80 s.
	firstAudio, _ := tl.FirstShared(2)
	firstVideo, _ := tl.FirstShared(1)
	if gapMs := float64(firstAudio-firstVideo) / 1e6; math.Abs(gapMs-2000) > 2 {
		t.Fatalf("expected 2000 ms start offset, got %.2f ms", gapMs)
	}

	var lastV, lastA int64
	var maxSkew float64
	for i := 0; i < 2000 && i < len(vRaw) && i < len(aRaw); i++ {
		vc, err := tl.Corrected(1, vRaw[i])
		if err != nil {
			t.Fatal(err)
		}
		ac, err := tl.Corrected(2, aRaw[i])
		if err != nil {
			t.Fatal(err)
		}
		if vc <= lastV || ac <= lastA {
			t.Fatalf("corrected output not monotonic at %d", i)
		}
		lastV, lastA = vc, ac
		skew := math.Abs(float64((ac - firstAudio) - (vc - firstVideo)))
		if skew > maxSkew {
			maxSkew = skew
		}
	}
	if maxSkew > float64(blockNs) {
		t.Fatalf("residual skew after correction too large: %.1f ms", maxSkew/1e6)
	}
}

func TestCorrectedRequiresCalibration(t *testing.T) {
	tl := New(1, Options{})
	tl.Observe(Observation{Track: 1, RawMs: 1})
	if _, err := tl.Corrected(1, 0); err == nil {
		t.Fatal("expected error before calibration")
	}
}

func TestCalibrateUnknownReference(t *testing.T) {
	tl := New(9, Options{})
	tl.Observe(Observation{Track: 1, RawMs: 1})
	if err := tl.Calibrate(); err == nil {
		t.Fatal("expected error for missing reference track")
	}
}

func TestAnchorPinsOffset(t *testing.T) {
	const blockNs = int64(40e6)
	tl := New(1, Options{MinSamples: 4})
	for _, o := range genFeed(100, 0, blockNs, 0) {
		tl.Observe(o)
	}
	for _, o := range genFeed(100, 0, blockNs, 0) {
		o.Track = 2
		tl.Observe(o)
	}
	// Anchor track 2's first raw timestamp to 500 ms on the shared clock.
	tl.Anchor(2, 0, 500*1e6)
	if err := tl.Calibrate(); err != nil {
		t.Fatal(err)
	}
	got, _ := tl.FirstShared(2)
	if want := int64(500 * 1e6); got != want {
		t.Fatalf("anchor not applied: got %d want %d", got, want)
	}
}

func TestDriftClampedToBound(t *testing.T) {
	tl := New(1, Options{MinSamples: 4, MaxDriftPPM: 1000})
	for _, o := range genFeed(500, 0, 40e6, 9000) { // 0.9% fast, above bound
		tl.Observe(o)
	}
	if err := tl.Calibrate(); err != nil {
		t.Fatal(err)
	}
	drift, _ := tl.DriftPPM(1)
	if drift > 1000.5 || drift < 999.5 {
		t.Fatalf("drift not clamped: %.1f ppm", drift)
	}
}

func TestTracksSorted(t *testing.T) {
	tl := New(1, Options{})
	for _, id := range []media.TrackID{3, 1, 2} {
		tl.Observe(Observation{Track: id, RawMs: 1})
	}
	got := tl.Tracks()
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("tracks not sorted: %v", got)
		}
	}
}
