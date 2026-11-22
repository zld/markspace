package clock

import (
	"errors"
	"math"
	"testing"

	"github.com/pairwise/mediad/internal/fault"
)

func TestConvertRescalesAndRounds(t *testing.T) {
	tl := NanosecondTimeline()
	got, err := tl.Convert(1, 90000)
	if err != nil || got != 11111 {
		t.Fatalf("Convert(1,90000)=%d,%v want 11111", got, err)
	}
	got, _ = tl.Convert(48000, 48000)
	if got != int64(NanosPerSecond) {
		t.Fatalf("Convert(48000,48000)=%d", got)
	}
	if _, err := tl.Convert(math.MaxUint64, 1000); err == nil {
		t.Fatal("expected overflow")
	}
	if _, err := tl.Convert(1, 0); err == nil {
		t.Fatal("expected zero-timescale error")
	}
}

func TestEditMapPassthroughEmptyAndShift(t *testing.T) {
	em := NewEditMap(nil)
	if v, err := em.Map(1234); err != nil || v != 1234 {
		t.Fatalf("passthrough=%d,%v", v, err)
	}
	// 1s empty edit then 2s of media starting at media tick 500.
	em = NewEditMap([]Edit{
		{SegmentDuration: NanosPerSecond, MediaTime: -1},
		{SegmentDuration: 2 * NanosPerSecond, MediaTime: 500},
	})
	if v, err := em.Map(500); err != nil || v != int64(NanosPerSecond) {
		t.Fatalf("first frame=%d,%v", v, err)
	}
	if v, err := em.Map(1500); err != nil || v != int64(NanosPerSecond)+1000 {
		t.Fatalf("offset frame=%d,%v", v, err)
	}
	if _, err := em.Map(10); err == nil {
		t.Fatal("media before edit should fail")
	}
	if end := em.PresentationEnd(); end != 3*int64(NanosPerSecond) {
		t.Fatalf("end=%d", end)
	}
}

func TestTrackClockMapsDTSAndCTO(t *testing.T) {
	tc, err := NewTrackClock("a.mp4", 1, 1000, NanosecondTimeline(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tc.Presentation(0, 0)
	if err != nil || got != 0 {
		t.Fatalf("first=%d,%v", got, err)
	}
	got, err = tc.Presentation(10, 5)
	if err != nil || got != 15_000_000 {
		t.Fatalf("cto=%d,%v", got, err)
	}
	if tc.DecodeEnd() != 10 {
		t.Fatalf("decode end=%d", tc.DecodeEnd())
	}
}

func TestTrackClockDetectsWrapRegressionOverflow(t *testing.T) {
	cases := []struct {
		name   string
		dts    []uint64
		cause  error
		sample int
	}{
		{"wrap", []uint64{100, math.MaxUint32 - 4, 8}, fault.ErrTimeWrap, 2},
		{"backwards", []uint64{1000, 900}, fault.ErrTimeBackwards, 1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clk, _ := NewTrackClock("b.mp4", 7, 90000, NanosecondTimeline(), nil, 0)
			var clockErr error
			for _, d := range tc.dts {
				if _, err := clk.Presentation(d, 0); err != nil {
					clockErr = err
					break
				}
			}
			if clockErr == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(clockErr, tc.cause) {
				t.Fatalf("cause=%v want %v", clockErr, tc.cause)
			}
			e, ok := fault.As(clockErr)
			if !ok || e.TrackID != 7 || e.Path != "b.mp4" || e.Sample != tc.sample || e.Offset != -1 {
				t.Fatalf("location wrong: %+v", e)
			}
		})
	}

	clk, _ := NewTrackClock("c.mp4", 3, 1000, NanosecondTimeline(), nil, 0)
	if _, err := clk.Presentation(5, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := clk.Presentation(6, math.MaxInt64); err == nil {
		t.Fatal("expected composition overflow")
	}
}

func TestTrackClockRejectsPresentationBeforeOrigin(t *testing.T) {
	clk, _ := NewTrackClock("d.mp4", 1, 1000, NanosecondTimeline(), nil, int64(NanosPerSecond))
	_, err := clk.Presentation(5, 0)
	if !errors.Is(err, fault.ErrTimeBackwards) {
		t.Fatalf("err=%v", err)
	}
}
