package clock

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
)

func TestTimelineNoWrap(t *testing.T) {
	tl, err := NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := tl.Extend(1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if origin != 1000 {
		t.Fatalf("origin = %d, want 1000", origin)
	}
	got, err := tl.Extend(4000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4000 {
		t.Fatalf("extended = %d, want 4000", got)
	}
	if tl.Wraps() != 0 {
		t.Fatalf("wraps = %d, want 0", tl.Wraps())
	}
}

func TestTimelineWrap(t *testing.T) {
	const start = 4294967000
	tl, err := NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Extend(start, 0); err != nil {
		t.Fatal(err)
	}
	// 296 ticks later the 32-bit counter wraps to zero.
	got, err := tl.Extend(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(start)+296 {
		t.Fatalf("extended = %d, want %d", got, uint64(start)+296)
	}
	if tl.Wraps() != 1 {
		t.Fatalf("wraps = %d, want 1", tl.Wraps())
	}
	// Continue past the wrap: the timeline must stay monotonic.
	prev, _ := tl.LastExtended()
	for i := 0; i < 50; i++ {
		got, err := tl.Extend(uint32(i+1)*3000, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got <= prev {
			t.Fatalf("timeline not monotonic at %d: %d <= %d", i, got, prev)
		}
		prev = got
	}
	if tl.Wraps() != 1 {
		t.Fatalf("wraps = %d, want 1", tl.Wraps())
	}
}

func TestTimelineReorderedBackStepKeepsClock(t *testing.T) {
	tl, err := NewTimeline(48000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Extend(50000, 0); err != nil {
		t.Fatal(err)
	}
	got, err := tl.Extend(49000, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The reordered packet reports its own earlier position...
	if got != 49000 {
		t.Fatalf("reordered packet placed at %d, want 49000", got)
	}
	// ...while the committed clock stays at the high-water mark.
	if last, _ := tl.LastExtended(); last != 50000 {
		t.Fatalf("committed clock moved to %d, want 50000", last)
	}
	if tl.BackStep() != 1000 {
		t.Fatalf("backstep = %d, want 1000", tl.BackStep())
	}
	got, err = tl.Extend(50500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 50500 {
		t.Fatalf("extended = %d, want 50500", got)
	}
}

func TestTimelineRejectsHugeJumpAndKeepsCommitted(t *testing.T) {
	tl, err := NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Extend(100, 0); err != nil {
		t.Fatal(err)
	}
	// Two billion ticks forward is beyond any plausible packet spacing.
	_, err = tl.Extend(2_000_000_100, 0)
	if err == nil {
		t.Fatal("expected out-of-range error")
	}
	if cat, ok := merrors.CategoryOf(err); !ok || cat != merrors.OutOfRange {
		t.Fatalf("category = %v (%v)", cat, err)
	}
	last, _ := tl.LastExtended()
	if last != 100 {
		t.Fatalf("committed time polluted: %d", last)
	}
	if tl.Wraps() != 0 {
		t.Fatalf("rejected jump counted as wrap: %d", tl.Wraps())
	}
}

func TestTimelineArrivalGuard(t *testing.T) {
	tl, err := NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Extend(0, 1_000_000); err != nil {
		t.Fatal(err)
	}
	// 90000 ticks correspond to 1s. Jumping 1s of media into 10ms of
	// arrival time is a device glitch and must be refused.
	if _, err := tl.Extend(10*90000, 1_010_000); err == nil {
		t.Fatal("expected forward-jump rejection")
	}
	last, _ := tl.LastExtended()
	if last != 0 {
		t.Fatalf("committed time polluted: %d", last)
	}
}

func TestTimelineDriftConfig(t *testing.T) {
	tl, err := NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.SetDrift(120); err != nil {
		t.Fatal(err)
	}
	if tl.DriftPPM() != 120 {
		t.Fatalf("drift = %d", tl.DriftPPM())
	}
	if err := tl.SetDrift(1_000_000); err == nil {
		t.Fatal("expected drift bound error")
	}
}

func TestSignExtend(t *testing.T) {
	if got := SignExtend(0, Modulus); got != Modulus {
		t.Fatalf("sign extend across wrap = %d, want %d", got, uint64(Modulus))
	}
	if got := SignExtend(5, 0); got != 5 {
		t.Fatalf("sign extend plain = %d", got)
	}
	if got := SignExtend(4294967295, 0); got != ^uint64(0) {
		t.Fatalf("sign extend negative = %d", got)
	}
}

func TestIntegerConversions(t *testing.T) {
	if got := TicksToMicros(90000, 90000); got != 1_000_000 {
		t.Fatalf("ticks->micros = %d", got)
	}
	if got := MicrosToTicks(1_000_000, 48000); got != 48000 {
		t.Fatalf("micros->ticks = %d", got)
	}
	if got := TicksToMicros(1, 90000); got != 11 {
		t.Fatalf("rounded-down micros = %d, want 11", got)
	}
	if _, err := NewTimeline(0); err == nil {
		t.Fatal("expected error for zero clock rate")
	}
}

func TestTimelineDeterministic(t *testing.T) {
	const raw0 = 4294966000
	run := func() ([]uint64, int) {
		tl, _ := NewTimeline(90000)
		var out []uint64
		for i := 0; i < 200; i++ {
			ts := raw0 + uint32(i*3000)
			got, err := tl.Extend(ts, 0)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, got)
		}
		return out, tl.Wraps()
	}
	a, wa := run()
	b, wb := run()
	if wa != wb || wa != 1 {
		t.Fatalf("wraps differ or wrong: %d %d", wa, wb)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at %d: %d vs %d", i, a[i], b[i])
		}
	}
}

func TestTimelineText(t *testing.T) {
	tl, err := NewTimeline(48000)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tl.Extend(42, 0)
	if tl.String() == "" {
		t.Fatal("empty String")
	}
}
