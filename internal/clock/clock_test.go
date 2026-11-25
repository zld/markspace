package clock

import "testing"

func TestUnwrapNextForwardWrap(t *testing.T) {
	const mod = uint64(1) << 8 // small modulus to exercise the math
	// Walk 250,255,0,5: must expand to 250,255,256,261.
	seq := []uint64{250, 255, 0, 5}
	want := []uint64{250, 255, 256, 261}
	var prev uint64
	have := false
	for i, raw := range seq {
		got := UnwrapNext(prev, have, raw, mod)
		if got != want[i] {
			t.Fatalf("step %d raw=%d: unwrapped=%d want %d", i, raw, got, want[i])
		}
		prev, have = got, true
	}
}

func TestTrackerReal33BitWrap(t *testing.T) {
	tr := NewTracker(Modulus33)
	before := Modulus33 - 18000
	if v, w := tr.Feed(before); w || v != before {
		t.Fatalf("first feed v=%d w=%v", v, w)
	}
	if v, w := tr.Feed(Modulus33 - 9000); w || v != Modulus33-9000 {
		t.Fatalf("pre-wrap v=%d w=%v", v, w)
	}
	v, w := tr.Feed(0) // segment boundary lands exactly on wrap
	if !w {
		t.Fatal("expected wrap at boundary")
	}
	if v != Modulus33 {
		t.Fatalf("post-wrap unwrapped=%d want %d", v, Modulus33)
	}
	v, w = tr.Feed(9000)
	if w || v != Modulus33+9000 {
		t.Fatalf("after wrap v=%d w=%v", v, w)
	}
	if tr.Wraps() != 1 {
		t.Fatalf("wraps=%d want 1", tr.Wraps())
	}
}

func TestMediaClockSharedTimelineAndCrossCheck(t *testing.T) {
	c := NewMediaClock()
	start := Modulus33 - 36000
	if _, w := c.FeedPCR(start); w {
		t.Fatal("unexpected wrap on first PCR")
	}
	if now, w := c.FeedPCR(0); !w || now != Tick(Modulus33) {
		t.Fatalf("boundary PCR now=%d wrap=%v, want %d/true", now, w, Modulus33)
	}
	// PTS tracker is independent but sits on the same 90 kHz timeline.
	c.FeedPTS(start)
	pts := c.FeedPTS(0)
	if pts != Tick(Modulus33) {
		t.Fatalf("pts unwrap=%d want %d", pts, Modulus33)
	}
	if _, ok := c.CheckAgainstPCR(Tick(Modulus33), 9000); !ok {
		t.Fatal("boundary PTS should align with PCR within tolerance")
	}
	if TicksToSeconds(Tick(TickRate)) != 1.0 {
		t.Fatal("90000 ticks must be 1s")
	}
	if PCRFull(1, 270) != 570 {
		t.Fatal("PCR full conversion wrong")
	}
}
