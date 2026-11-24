package slice

import (
	"testing"

	"mediad/internal/clock"
	"mediad/ts"
)

func ev(track string, idx int64, payloadLen int) ts.TrackEvent {
	payload := make([]byte, payloadLen)
	return ts.TrackEvent{TrackID: track, Start: idx * 90, End: (idx + 1) * 90, Payload: payload}
}

func TestSlicerFixedBoundaries(t *testing.T) {
	// 5 ms target, zero tolerance, 100 bytes per 1 ms packet.
	s, err := New(Config{TargetTicks: clock.FromMs(5), ToleranceTicks: 0, TargetBitrateBPS: 800000})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 10; i++ {
		s.Observe(ev("a", i, 100), nil)
	}
	got := s.Flush()
	if len(got) != 2 {
		t.Fatalf("slices = %d, want 2", len(got))
	}
	for i, sl := range got {
		wantStart := int64(i) * clock.FromMs(5)
		if sl.Start != wantStart || sl.End != wantStart+clock.FromMs(5) {
			t.Fatalf("slice %d span [%d,%d], want 5 ms at %d", i, sl.Start, sl.End, wantStart)
		}
		if sl.BoundaryErrorMs() > 1 {
			t.Fatalf("alignment error %d ms exceeds 1 ms", sl.BoundaryErrorMs())
		}
		if sl.DurationMs() != 5 {
			t.Fatalf("duration = %d ms, want 5", sl.DurationMs())
		}
	}
}

func TestSlicerToleranceLandsBoundary(t *testing.T) {
	// Target 5 ms but packets close at 4 ms; tolerance of 1 ms permits it.
	s, _ := New(Config{TargetTicks: clock.FromMs(5), ToleranceTicks: clock.FromMs(1), TargetBitrateBPS: 1000})
	for i := int64(0); i < 4; i++ {
		s.Observe(ev("a", i, 50), nil)
	}
	got := s.Flush()
	if len(got) != 1 || got[0].DurationMs() != 4 {
		t.Fatalf("want one 4 ms slice inside tolerance, got %+v", got)
	}
	if got[0].BoundaryErrorMs() > 1 {
		t.Fatalf("boundary error %d ms must stay within 1 ms", got[0].BoundaryErrorMs())
	}
}

func TestSlicerBoundaryDoesNotCrossError(t *testing.T) {
	s, _ := New(Config{TargetTicks: clock.FromMs(10), ToleranceTicks: 0, TargetBitrateBPS: 1000})
	errs := []clock.RangeError{{Kind: clock.KindDiscontinuity, Start: clock.FromMs(3), End: clock.FromMs(4), Msg: "gap"}}
	for i := int64(0); i < 10; i++ {
		var e []clock.RangeError
		if i == 3 {
			e = errs
		}
		s.Observe(ev("a", i, 10), e)
	}
	got := s.Flush()
	if len(got) < 2 {
		t.Fatalf("error must force an early boundary, got %+v", got)
	}
	// First slice ends exactly at the error start; no slice spans the gap.
	for _, sl := range got {
		if sl.Start < clock.FromMs(3) && sl.End > clock.FromMs(3) {
			t.Fatalf("slice %+v crosses the error boundary at 3 ms", sl)
		}
	}
	if got[0].End != clock.FromMs(3) {
		t.Fatalf("first slice end = %d, want %d", got[0].End, clock.FromMs(3))
	}
	if len(s.Errors()) != 1 {
		t.Fatalf("error must be retained in slicer diagnostics: %+v", s.Errors())
	}
}

func TestSlicerBitrate(t *testing.T) {
	// 100 bytes per 1 ms packet => 100_000 bytes/s => 800_000 bps.
	s, _ := New(Config{TargetTicks: clock.FromMs(5), ToleranceTicks: 0, TargetBitrateBPS: 800000})
	for i := int64(0); i < 5; i++ {
		s.Observe(ev("a", i, 100), nil)
	}
	got := s.Flush()
	if len(got) != 1 {
		t.Fatalf("slices = %d", len(got))
	}
	if got[0].ActualBitrateBPS != 800000 {
		t.Fatalf("actual bitrate = %d, want 800000", got[0].ActualBitrateBPS)
	}
	if got[0].TargetBitrateBPS != 800000 {
		t.Fatalf("target bitrate not propagated: %d", got[0].TargetBitrateBPS)
	}
}

func TestSlicerConfigValidation(t *testing.T) {
	if _, err := New(Config{TargetTicks: 0}); err == nil {
		t.Fatal("zero target must be rejected")
	}
	if _, err := New(Config{TargetTicks: 100, ToleranceTicks: 100}); err == nil {
		t.Fatal("tolerance >= target must be rejected")
	}
	if _, err := New(Config{TargetTicks: 100, ToleranceTicks: -1}); err == nil {
		t.Fatal("negative tolerance must be rejected")
	}
}

func TestSlicerPerTrackIndependence(t *testing.T) {
	s, _ := New(Config{TargetTicks: clock.FromMs(2), ToleranceTicks: 0, TargetBitrateBPS: 1000})
	for i := int64(0); i < 4; i++ {
		s.Observe(ev("a", i, 10), nil)
		s.Observe(ev("b", i, 20), nil)
	}
	got := s.Flush()
	if len(got) != 4 { // 2 slices per track
		t.Fatalf("slices = %d, want 4", len(got))
	}
}
