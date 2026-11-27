package timebase_test

import (
	"math"
	"testing"

	"archivecheck/internal/timebase"
)

func TestFrameDurations(t *testing.T) {
	cases := []struct {
		rate timebase.Rate
		want timebase.Nanos
	}{
		{timebase.FPS24, 41666667},
		{timebase.FPS25, 40000000},
		{timebase.FPS2997, 33366667},
		{timebase.FPS50, 20000000},
	}
	for _, c := range cases {
		if got := c.rate.FrameDuration(); got != c.want {
			t.Errorf("%d/%d frame duration = %d, want %d", c.rate.Num, c.rate.Den, got, c.want)
		}
	}
}

func TestTimecodeToNanos(t *testing.T) {
	if got, err := timebase.FPS24.TimecodeToNanos(24); err != nil || got != timebase.Nanos(1000000000) {
		t.Errorf("24 frames at 24 fps = %d, %v", got, err)
	}
	if got, err := timebase.FPS25.TimecodeToNanos(93079); err != nil || got != 3723160000000 {
		t.Errorf("93079 frames at 25 fps = %d, %v", got, err)
	}
	// 29.97: 30000 real frames is exactly 1001 seconds.
	if got, err := timebase.FPS2997.TimecodeToNanos(30000); err != nil || got != 1001*1000000000 {
		t.Errorf("30000 frames at 29.97 = %d, %v", got, err)
	}
	if _, err := timebase.FPS25.TimecodeToNanos(-1); err == nil {
		t.Error("negative frame count should fail")
	}
	if _, err := timebase.FPS25.TimecodeToNanos(math.MaxInt64); err == nil {
		t.Error("overflowing frame count should fail")
	}
	if _, err := (timebase.Rate{Num: 7, Den: 3}).TimecodeToNanos(1); err == nil {
		t.Error("unsupported rate should fail")
	}
}

func TestNanosToFramesRoundTrip(t *testing.T) {
	for _, r := range []timebase.Rate{timebase.FPS24, timebase.FPS25, timebase.FPS2997, timebase.FPS50} {
		for _, frames := range []int64{0, 1, 999, 86400} {
			n, err := r.TimecodeToNanos(frames)
			if err != nil {
				t.Fatalf("%v: %v", r, err)
			}
			back, err := r.NanosToFrames(n)
			if err != nil {
				t.Fatalf("%v: %v", r, err)
			}
			// Rounding to nanoseconds may land one frame early at worst.
			if back != frames && back != frames-1 {
				t.Errorf("%v: %d frames round-tripped to %d", r, frames, back)
			}
		}
	}
	if _, err := timebase.FPS25.NanosToFrames(-1); err == nil {
		t.Error("negative timestamp should fail")
	}
	if _, err := timebase.FPS25.NanosToFrames(1 << 62); err == nil {
		t.Error("huge timestamp should fail")
	}
}

func TestRangesAndClock(t *testing.T) {
	a := timebase.Range{Start: 10, End: 20}
	b := timebase.Range{Start: 15, End: 30}
	if !a.Valid() || a.Duration() != 10 {
		t.Fatalf("range invalid: %+v", a)
	}
	if !a.Overlaps(b) || a.Overlaps(timebase.Range{Start: 20, End: 25}) {
		t.Error("overlap detection wrong")
	}
	if m := a.Merge(b); m.Start != 10 || m.End != 30 {
		t.Errorf("merge = %+v", m)
	}
	c := timebase.NewClock(1000)
	if got := c.At(250); got != 1250 {
		t.Errorf("At = %d", got)
	}
	if got := c.Relative(1250); got != 250 {
		t.Errorf("Relative = %d", got)
	}
}
