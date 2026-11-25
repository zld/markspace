package clock

import "testing"

func TestExpanderWraparound(t *testing.T) {
	var e Expander
	cases := []struct {
		raw uint32
		exp uint64
	}{
		{0xFFFFFFF0, 0xFFFFFFF0},
		{0xFFFFFFF8, 0xFFFFFFF8},
		{0x00000004, 0x100000004},
		{0x00000010, 0x100000010},
	}
	for _, c := range cases {
		if got := e.Feed(c.raw); got != c.exp {
			t.Fatalf("Feed(%#x) = %#x, want %#x", c.raw, got, c.exp)
		}
	}
}

func TestExpanderEqualAndBackward(t *testing.T) {
	var e Expander
	first := e.Feed(1000)
	if got := e.Feed(1000); got != first {
		t.Fatalf("equal timestamp moved: %d != %d", got, first)
	}
	// A small backward jump is a real reorder, not wraparound.
	if got := e.Feed(990); got != first-10 {
		t.Fatalf("backward step = %d, want %d", got, first-10)
	}
}

func TestTimelineAnchorAndTrust(t *testing.T) {
	tl := NewTimeline(90000)
	if tl.Trusted() {
		t.Fatal("new timeline must not be trusted without RTCP")
	}
	if _, err := tl.ToWall(0); err != ErrNoAnchor {
		t.Fatalf("ToWall before anchor err = %v, want ErrNoAnchor", err)
	}
	tl.Feed(9000)
	tl.BindAnchor(SenderAnchor{RTPTimestamp: 9000, NTPSeconds: 1000.5})
	if !tl.Trusted() {
		t.Fatal("timeline must be trusted after BindAnchor")
	}
	// 9000 ticks later at 90kHz is 0.1s.
	wall, err := tl.ToWall(tl.Feed(18000))
	if err != nil {
		t.Fatal(err)
	}
	if d := wall - 1000.6; d > 1e-9 || d < -1e-9 {
		t.Fatalf("wall = %v, want 1000.6", wall)
	}
}

func TestTimelineWraparoundWithAnchor(t *testing.T) {
	tl := NewTimeline(90000)
	tl.Feed(0xFFFFFFF0)
	tl.BindAnchor(SenderAnchor{RTPTimestamp: 0xFFFFFFF0, NTPSeconds: 0})
	got := tl.Feed(0x10) // 32 ticks forward across the wrap
	want := int64(0x100000010)
	if got != want {
		t.Fatalf("expanded across wrap = %d, want %d", got, want)
	}
	wall, err := tl.ToWall(got)
	if err != nil {
		t.Fatal(err)
	}
	if d := wall - 32.0/90000.0; d > 1e-12 || d < -1e-12 {
		t.Fatalf("wall across wrap = %v", wall)
	}
	raw, ok := tl.OriginalTS()
	if !ok || raw != 0x10 {
		t.Fatalf("original timestamp not preserved: %#x ok=%v", raw, ok)
	}
}
