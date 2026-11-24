package clock

import (
	"math"
	"testing"

	"mediad/internal/mederr"
)

func TestNormalizeVFRExplicitBoundaries(t *testing.T) {
	// Deliberately uneven 24/30-ish intervals; no average frame rate is used.
	pts := []int64{0, 16_666_667, 50_000_001, 66_666_668}
	var errs mederr.List
	frames := NormalizeVFR(pts, NanoScale, "vfr:test", 33_333_332, 250_000_000, &errs)
	if errs.Len() != 0 || errs.HasFatal() {
		t.Fatalf("unexpected errors: %+v", errs.Errs)
	}
	if len(frames) != 4 {
		t.Fatalf("got %d frames", len(frames))
	}
	wantDurations := []int64{16_666_667, 33_333_334, 16_666_667, 33_333_332}
	for i, want := range wantDurations {
		if frames[i].DurationNs != want {
			t.Fatalf("frame %d duration %d want %d", i, frames[i].DurationNs, want)
		}
	}
	if end := VFREndNs(frames); end != 100_000_000 {
		t.Fatalf("end %d want 100ms", end)
	}
}

func TestNormalizeVFRExtremeAndNonMonotonicLocated(t *testing.T) {
	pts := []int64{0, 20_000_000, 15_000_000, 40_000_000, 400_000_000}
	var errs mederr.List
	frames := NormalizeVFR(pts, NanoScale, "vfr:bad", 20_000_000, 100_000_000, &errs)
	if len(frames) != 4 { // 0, 20ms, 40ms, 400ms; backward 15ms skipped
		t.Fatalf("got %d frames: %+v", len(frames), frames)
	}
	if errs.Len() != 2 {
		t.Fatalf("got %d errors, want non-monotonic and extreme: %+v", errs.Len(), errs.Errs)
	}
	for _, e := range errs.Errs {
		if e.Track != "vfr:bad" || e.Category != mederr.CatTimeline || !e.Recoverable {
			t.Fatalf("error not locatable/recoverable: %+v", e)
		}
		if e.StartNanos < 0 {
			t.Fatalf("error lacks time range: %+v", e)
		}
	}
}

func TestSnapToSamplesUnderTwoMilliseconds(t *testing.T) {
	// Every rational boundary used by the demo, including odd EDL points,
	// must land within one 48 kHz sample after snapping.
	boundaries := []int64{0, 1_500_000_000, 3_500_000_000, 4_000_000_000, 11_000_000_000}
	max := MaxSnapErrorNs(boundaries, 48000)
	if max > 1_000_000_000/48000/2+1 {
		t.Fatalf("snap error %d ns too large", max)
	}
	if math.Abs(Millis(max)) > 0.02 {
		t.Fatalf("alignment %.4f ms exceeds one-sample bound", Millis(max))
	}
}
