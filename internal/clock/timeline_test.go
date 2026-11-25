package clock

import "testing"

func TestTimelineConversions(t *testing.T) {
	tl := NewForSampleRate(48000)
	if got, want := tl.TicksToSamples(48000), int64(48000); got != want {
		t.Fatalf("ticks->samples = %d, want %d", got, want)
	}
	if got, want := tl.SamplesToTicks(24000), int64(24000); got != want {
		t.Fatalf("samples->ticks = %d, want %d", got, want)
	}
	if got, want := tl.TicksToMillis(24000), int64(500); got != want {
		t.Fatalf("ticks->millis = %d, want %d", got, want)
	}
	if got, want := tl.MillisToTicks(500), int64(24000); got != want {
		t.Fatalf("millis->ticks = %d, want %d", got, want)
	}
}

func TestAlignDownNeverPassesRequest(t *testing.T) {
	// 90kHz ticks with 48kHz samples: 1 sample = 1.875 ticks.
	tl, err := New(MustRate(90000, 1), 48000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ticks := range []int64{0, 1, 2, 15, 90000, 90001} {
		s := tl.AlignDownSamples(ticks)
		if !sampleAtOrBefore(tl, s, ticks) {
			t.Fatalf("align-down(%d) -> sample %d starts after the request", ticks, s)
		}
		if sampleAtOrBefore(tl, s+1, ticks) {
			t.Fatalf("align-down(%d) -> %d is not the latest sample at/before", ticks, s)
		}
	}
}

func TestAlignUpCoversRequest(t *testing.T) {	tl, err := New(MustRate(90000, 1), 48000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ticks := range []int64{0, 1, 2, 3, 90000} {
		s := tl.AlignUpSamples(ticks)
		if tl.SamplesToTicks(s) < ticks {
			t.Fatalf("align-up(%d) -> sample %d does not cover", ticks, s)
		}
	}
}

func TestNegativeTicksFloor(t *testing.T) {
	tl := NewForSampleRate(1000)
	if got, want := tl.TicksToMillis(-1), int64(-1); got != want {
		t.Fatalf("negative floor = %d, want %d", got, want)
	}
	if got, want := tl.TicksToSamples(-1500), int64(-1500); got != want {
		t.Fatalf("samples(-1500) = %d, want %d", got, want)
	}
}

func TestUnwrapSigned(t *testing.T) {
	cases := []struct{ diff, want int64 }{
		{5, 5},
		{0xFFFFFFFF, -1},
		{0x80000000, -1 << 31},
		{0x7FFFFFFF, 1<<31 - 1},
	}
	for _, c := range cases {
		if got := UnwrapSigned(c.diff); got != c.want {
			t.Fatalf("UnwrapSigned(%#x) = %d, want %d", c.diff, got, c.want)
		}
	}
}

func TestWrapsForward(t *testing.T) {
	if !WrapsForward(0xFFFFFFF0, 0x10) {
		t.Fatal("expected forward wrap")
	}
	if WrapsForward(10, 20) {
		t.Fatal("ordinary increment is not a wrap")
	}
	if WrapsForward(20, 10) {
		t.Fatal("plain reorder is not a wrap")
	}
}

func TestBadRates(t *testing.T) {
	if _, err := New(Rate{Num: 0, Den: 1}, 48000); err == nil {
		t.Fatal("expected error for zero tick rate")
	}
	if _, err := New(MustRate(90000, 1), 0); err == nil {
		t.Fatal("expected error for zero sample rate")
	}
}

// sampleAtOrBefore reports whether sample k starts at or before tick t using
// exact rational arithmetic; SamplesToTicks floors, which is too coarse here.
func sampleAtOrBefore(tl *Timeline, k, t int64) bool {
	return k*tl.Rate().Num <= t*tl.Rate().Den*int64(tl.SampleRate())
}
