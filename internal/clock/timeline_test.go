package clock

import (
	"math"
	"testing"
	"time"
)

func TestTicksToNsExactAndRounded(t *testing.T) {
	cases := []struct {
		timescale uint32
		ticks     int64
		want      int64
	}{
		{1, 0, 0},
		{1000, 1, 1_000_000},
		{90000, 1, 11_111},        // 11111.11 -> 11111
		{90000, 3, 33_333},        // 33333.33
		{90000, 5, 55_556},        // 55555.55 -> 55556
		{48000, 1, 20_833},        // 20833.33
		{48000, 3, 62_500},        // exact
		{44100, 1024, 23_219_955}, // 23219954.6 -> 23219955
		{1_000_000_000, 1, 1},
		{1000, -1, -1_000_000},
	}
	for _, c := range cases {
		if got := TicksToNs(c.timescale, c.ticks); got != c.want {
			t.Errorf("TicksToNs(%d,%d)=%d want %d", c.timescale, c.ticks, got, c.want)
		}
	}
}

func TestCumulativeRoundingUnderOneMillisecond(t *testing.T) {
	// Simulate 3 hours at 90 kHz with per-sample accumulation versus absolute
	// conversion. Absolute conversion must stay within 1 ms of wall time and
	// the accumulated variant is tracked only to prove the budget is met.
	const timescale = 90000
	const seconds = 3 * 3600
	const delta = 3003 // 29.97 fps
	stepNs := TicksToNs(timescale, delta)
	var accumulated int64
	var ticks int64
	worst := int64(0)
	for s := int64(0); s < seconds*int64(timescale)/delta; s++ {
		accumulated += stepNs
		ticks += delta
		if s%1000 == 0 {
			abs := TicksToNs(timescale, ticks)
			if d := abs64(abs - accumulated); d > worst {
				worst = d
			}
		}
	}
	if worst > int64(time.Millisecond) {
		t.Fatalf("accumulated rounding error %d ns exceeds 1 ms budget", worst)
	}
	// Exactness of absolute conversion: error under half a nanosecond per tick.
	for _, tick := range []int64{1, 1234567, 987654321, 3 * 3600 * 90000} {
		got := TicksToNs(timescale, tick)
		want := float64(tick) * 1e9 / float64(timescale)
		if d := math.Abs(float64(got) - want); d > 0.5+1e-6 {
			t.Fatalf("tick %d: got %d want %.3f (delta %.6f)", tick, got, want, d)
		}
	}
}

func TestNsRoundTripWithinTick(t *testing.T) {
	for _, ts := range []uint32{600, 1000, 90000, 48000, 44100, 1_000_000_000} {
		for _, ns := range []int64{0, 1, 1_000, 1e9, 12_345_678_901} {
			ticks := NsToTicks(ts, ns)
			back := TicksToNs(ts, ticks)
			if d := abs64(back - ns); d > int64(NsPerSecond)/int64(ts)+1 {
				t.Fatalf("ts=%d ns=%d roundtrip=%d delta=%d", ts, ns, back, d)
			}
		}
	}
}

func TestEditListPresentationMapping(t *testing.T) {
	el := EditList{
		MediaTimescale: 90000,
		MovieTimescale: 1000,
		Entries: []EditEntry{
			{SegmentDuration: 500, MediaTime: -1, RawOffset: 0},     // 0.5 s gap
			{SegmentDuration: 2000, MediaTime: 9000, RawOffset: 16}, // preserved start offset
		},
	}
	if got := el.FirstMediaTime(); got != 9000 {
		t.Fatalf("FirstMediaTime=%d want 9000", got)
	}
	if got := el.LeadNs(); got != 500*int64(time.Millisecond) {
		t.Fatalf("LeadNs=%d", got)
	}
	// First media tick 9000 appears at 0.5 s presentation.
	ns, ok := el.PresentationNs(9000)
	if !ok || ns != 500_000_000 {
		t.Fatalf("PresentationNs(9000)=(%d,%v)", ns, ok)
	}
	// Midway through the second edit: 0.5s + 1.0s.
	ns, ok = el.PresentationNs(9000 + 90000)
	if !ok || ns != 1_500_000_000 {
		t.Fatalf("PresentationNs mid=(%d,%v)", ns, ok)
	}
	// Outside edited range is reported as uncovered.
	if _, ok := el.PresentationNs(9000 + 2000*90); ok {
		t.Fatal("expected edit list to reject out-of-range media time")
	}
	// Inverse mapping.
	ticks, ok := el.MediaTicksAtNs(1_500_000_000)
	if !ok || ticks != 99000 {
		t.Fatalf("MediaTicksAtNs=(%d,%v)", ticks, ok)
	}
}

func TestPreservedStartOffsetInBase(t *testing.T) {
	b, err := NewBase(90000, 5*int64(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.ToNs(9000); got != int64(5*time.Second)+100_000_000 {
		t.Fatalf("ToNs=%d", got)
	}
	if got := b.FromNs(int64(5*time.Second) + 100_000_000); got != 9000 {
		t.Fatalf("FromNs=%d", got)
	}
}

func TestInvalidTimescale(t *testing.T) {
	if _, err := NewBase(0, 0); err == nil {
		t.Fatal("expected error for zero timescale")
	}
	if _, err := NewBase(2_000_000_000, 0); err == nil {
		t.Fatal("expected error for absurd timescale")
	}
}

func TestErrorExitCodes(t *testing.T) {
	if ExitCode(nil) != 0 {
		t.Fatal("nil error should be success")
	}
	if ExitCode(New(StageArgs, CodeBadArgs, "x")) != 1 {
		t.Fatal("args stage should exit 1")
	}
	if ExitCode(New(StageParse, CodeBadManifest, "x")) != 1 {
		t.Fatal("parse stage should exit 1")
	}
	if ExitCode(New(StageRepair, CodeUnrepairableRange, "x")) != 2 {
		t.Fatal("repair stage should exit 2")
	}
	pe, ok := As(New(StageScan, CodeTruncatedTable, "x").WithTrack(2).WithOffset(99))
	if !ok || pe.TrackID != 2 || pe.RawOffset != 99 {
		t.Fatalf("structured error lost: %+v", pe)
	}
}
