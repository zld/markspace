package clock

import (
	"testing"

	"archive.example/mediakernel/internal/events"
)

func TestMapTicksExactSeconds(t *testing.T) {
	ns, err := MapTicks(events.TrackVideo, 1500, 30000, 0, MaxTimelineNS, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 50_000_000 {
		t.Fatalf("got %d want 50000000", ns)
	}
}

func TestMapTicksRoundsHalfAwayFromZero(t *testing.T) {
	// 1 tick at 30kHz = 33333.33ns -> 33333; 2 ticks = 66666.67 -> 66667.
	if ns, _ := MapTicks(events.TrackVideo, 1, 30000, 0, MaxTimelineNS, 1, 0); ns != 33333 {
		t.Fatalf("got %d", ns)
	}
	if ns, _ := MapTicks(events.TrackVideo, 2, 30000, 0, MaxTimelineNS, 1, 0); ns != 66667 {
		t.Fatalf("got %d", ns)
	}
	// 1 tick at 48kHz = 20833.33 -> 20833.
	if ns, _ := MapTicks(events.TrackAudio, 1, 48000, 0, MaxTimelineNS, 1, 0); ns != 20833 {
		t.Fatalf("got %d", ns)
	}
}

func TestMapTicksOriginShift(t *testing.T) {
	ns, err := MapTicks(events.TrackSubtitle, 10, 1000, 1_000_000_000, MaxTimelineNS, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 1_010_000_000 {
		t.Fatalf("got %d", ns)
	}
}

func TestMapTicksRejectsZeroTimescale(t *testing.T) {
	_, err := MapTicks(events.TrackAudio, 5, 0, 0, MaxTimelineNS, 3, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Code != events.CodeInvalidTimescale || err.Track != events.TrackAudio || err.Seq != 3 || err.Offset != 100 {
		t.Fatalf("error lost context: %v", err)
	}
	if !err.Fatal() {
		t.Fatal("invalid timescale must be fatal")
	}
}

func TestMapTicksRejectsOutOfRange(t *testing.T) {
	_, err := MapTicks(events.TrackVideo, 10_000_000_000_000, 1000, 0, MaxTimelineNS, 4, 1)
	if err == nil || err.Code != events.CodeTimeOutOfRange {
		t.Fatalf("expected out-of-range, got %v", err)
	}
	// A negative origin that pushes result below the limit is rejected too.
	_, err = MapTicks(events.TrackVideo, 0, 1000, -MaxTimelineNS-1, MaxTimelineNS, 5, 2)
	if err == nil || err.Code != events.CodeTimeOutOfRange {
		t.Fatalf("expected out-of-range, got %v", err)
	}
}

func TestMapTicksDoesNotOverflowForHugeTicks(t *testing.T) {
	// MaxInt64 ticks at a 1kHz scale: must be rejected, never wrap.
	_, err := MapTicks(events.TrackVideo, 1<<62, 1000, 0, MaxTimelineNS, 6, 3)
	if err == nil || err.Code != events.CodeTimeOutOfRange {
		t.Fatalf("expected out-of-range, got %v", err)
	}
}

func TestUnwrap32(t *testing.T) {
	cases := []struct {
		anchor, raw uint32
		want        int64
	}{
		{0, 0, 0},
		{10, 20, 20},
		{20, 10, 10},
		{1_000_000, 4_294_967_000, -296}, // near the top of the range reads as just-before-anchor
		{4_294_967_000, 100, 4_294_967_396}, // wrapped forward across the 32-bit boundary
		{100, 4_294_967_000, -296},          // late pre-roll wrapped backwards
	}
	for _, c := range cases {
		if got := Unwrap32(c.anchor, c.raw); got != c.want {
			t.Fatalf("Unwrap32(%d,%d)=%d want %d", c.anchor, c.raw, got, c.want)
		}
	}
}

func TestUnwrap32PureFunctionOfAnchorAndRaw(t *testing.T) {
	// Same pair always yields the same value; arrival order is irrelevant.
	for i := 0; i < 100; i++ {
		if Unwrap32(4_294_967_000, uint32(i)) != Unwrap32(4_294_967_000, uint32(i)) {
			t.Fatal("Unwrap32 not deterministic")
		}
	}
}

func TestConverter32WithoutAnchorIsUnsigned(t *testing.T) {
	c, err := NewConverter(Options{Track: events.TrackRTP, Timescale: 90000, Width: Width32})
	if err != nil {
		t.Fatal(err)
	}
	ns, cerr := c.ToNS32(90000, 1, 0)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if ns != 1_000_000_000 {
		t.Fatalf("got %d", ns)
	}
}

func TestConverter32AnchorWrapsBackwards(t *testing.T) {
	c, cerr := NewConverter(Options{Track: events.TrackRTP, Timescale: 90000, Width: Width32, Anchor: 100, HasAnchor: true})
	if cerr != nil {
		t.Fatal(cerr)
	}
	// Raw 4_294_967_000 relative to anchor 100 means "296 ticks before".
	ns, e := c.ToNS32(4_294_967_000, 2, 0)
	if e != nil {
		t.Fatal(e)
	}
	if want := int64(-3_288_889); ns != want {
		t.Fatalf("got %d want %d", ns, want)
	}
}

func TestConverterRejectsInvalidOptions(t *testing.T) {
	if _, err := NewConverter(Options{Track: events.TrackVideo, Timescale: 0, Width: Width64}); err == nil {
		t.Fatal("expected invalid timescale")
	}
	if _, err := NewConverter(Options{Track: events.TrackVideo, Timescale: 1000, Width: 7}); err == nil {
		t.Fatal("expected unsupported width")
	}
	if _, err := NewConverter(Options{Track: events.TrackVideo, Timescale: 1000, Width: Width64, OriginNS: MaxTimelineNS + 1}); err == nil {
		t.Fatal("expected origin out of range")
	}
}

func TestDurationNS(t *testing.T) {
	ns, err := DurationNS(9000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 100_000_000 {
		t.Fatalf("got %d", ns)
	}
	if _, err := DurationNS(1, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestTicksFromNSRoundTrip(t *testing.T) {
	ns := int64(2_000_000_000)
	ticks, err := TicksFromNS(events.TrackAudio, ns, 48000, 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 96000 {
		t.Fatalf("got %d", ticks)
	}
	back, err := MapTicks(events.TrackAudio, ticks, 48000, 0, MaxTimelineNS, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if back != ns {
		t.Fatalf("round trip got %d want %d", back, ns)
	}
}
