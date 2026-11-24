package clock

import "testing"

func TestAudioRateConversionsExact(t *testing.T) {
	cases := []struct {
		rate  int64
		units int64
		want  Ticks
	}{
		{48000, 48000, 90000}, // one second
		{48000, 1, 2},         // 1 sample -> 1.875 ticks -> 2
		{96000, 96000, 90000},
		{44100, 44100, 90000},
		{44100, 441, 900}, // 10 ms
	}
	for _, c := range cases {
		if got := FromSeconds(c.units, c.rate); got != c.want {
			t.Errorf("FromSeconds(%d, %d) = %d, want %d", c.units, c.rate, got, c.want)
		}
		if got := ToRate(c.want, c.rate); got != c.units {
			t.Errorf("ToRate(%d, %d) = %d, want %d", c.want, c.rate, got, c.units)
		}
	}
}

func TestFrameIntervals(t *testing.T) {
	if got := FrameInterval(30); got != 3000 {
		t.Errorf("30fps interval = %d, want 3000", got)
	}
	// 24fps = 3750 ticks exactly (90000/24).
	if got := FrameInterval(24); got != 3750 {
		t.Errorf("24fps interval = %d, want 3750", got)
	}
	// 29.97-like fractional rate must round deterministically.
	a := FromSeconds(1001, 30000)
	b := FromSeconds(1001, 30000)
	if a != b {
		t.Fatalf("fractional conversion not deterministic: %d != %d", a, b)
	}
}

func TestRoundHalfAwayFromZero(t *testing.T) {
	// 1 tick at 48k = 0.5333 source units -> rounds to 1.
	if got := ToRate(1, 48000); got != 1 {
		t.Errorf("ToRate(1,48000) = %d, want 1", got)
	}
	// Negative values round away from zero, never toward it.
	if got := FromSeconds(-1, 48000); got != -2 {
		t.Errorf("FromSeconds(-1,48000) = %d, want -2", got)
	}
}

func TestNonZeroStartAndOrdering(t *testing.T) {
	// A track starting at 5s keeps its absolute offset on the shared line.
	start := FromSeconds(5, 1)
	first := start + FrameInterval(30)
	if Millis(first) != 5033 {
		t.Errorf("first frame at %d ms, want 5033", Millis(first))
	}
	a := OrderKey{At: 100, TrackID: "vid-b", FrameIndex: 0}
	b := OrderKey{At: 100, TrackID: "vid-a", FrameIndex: 7}
	c := OrderKey{At: 99, TrackID: "vid-a", FrameIndex: 0}
	if !c.Less(b) || !b.Less(a) {
		t.Fatal("stable ordering by tick, then track id, then frame index broken")
	}
}

func TestInvalidRatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on zero rate")
		}
	}()
	FromSeconds(1, 0)
}
