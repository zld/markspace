package clock_test

import (
	"errors"
	"testing"

	"mediacore/internal/clock"
)

func TestEditMapHandlesEmptySegmentAndMediaOffset(t *testing.T) {
	entries := []clock.EditEntry{
		{Duration: 2_000, MediaTime: -1, Offset: 41},
		{Duration: 720_000, MediaTime: 250, Offset: 73},
	}
	tm, err := clock.BuildEditMap("x.mp4", 1, 1000, 1000, entries)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := tm.EventToMedia(1_000_000_000); err != nil || ok {
		t.Fatalf("empty edit mapping ok=%v err=%v", ok, err)
	}
	media, ok, err := tm.EventToMedia(600_000_000_000)
	if err != nil || !ok {
		t.Fatalf("main edit mapping: media=%d ok=%v err=%v", media, ok, err)
	}
	if media != 598_250 {
		t.Fatalf("media = %d, want 598250 after empty gap and offset", media)
	}
	ev, err := tm.MediaToEvent(598_250)
	if err != nil || ev != 600_000_000_000 {
		t.Fatalf("inverse event=%d err=%v", ev, err)
	}
	for _, m := range []clock.MediaTime{250, 10_000, 598_250, 719_250} {
		tickErr, err := tm.RoundTripError(m)
		if err != nil {
			t.Fatal(err)
		}
		if tickErr > 1 {
			t.Fatalf("round trip tick error %d > 1 for %d", tickErr, m)
		}
	}
}

func TestTimescaleRoundingStaysWithinOneTick(t *testing.T) {
	s := clock.TicksPerSecond(90_000)
	for _, ns := range []int64{0, 1, 11_111, 999_999_999, 600_000_000_000} {
		ticks := s.FromNS(clock.EventTime(ns))
		back := s.NS(ticks)
		diff := ns - int64(back)
		if diff < 0 {
			diff = -diff
		}
		if diff > int64(s.NS(1)) {
			t.Fatalf("rounding error %dns exceeds one tick %dns", diff, s.NS(1))
		}
	}
}

func TestNegativeDurationIsLocated(t *testing.T) {
	_, err := clock.BuildEditMap("bad.mp4", 7, 1000, 1000, []clock.EditEntry{
		{Duration: -1, MediaTime: 0, Offset: 123},
	})
	var ce *clock.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %T is not *clock.Error", err)
	}
	if ce.Category != clock.CategoryNegativeDuration || ce.Offset != 123 || ce.Track != 7 || ce.Path != "bad.mp4" {
		t.Fatalf("bad located error: %+v", ce)
	}
}
