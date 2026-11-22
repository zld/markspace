package clock

import (
	"errors"
	"math"
	"testing"
)

func TestTimeConversions(t *testing.T) {
	sec, err := FromSeconds(7200)
	if err != nil {
		t.Fatalf("FromSeconds: %v", err)
	}
	if sec != 7_200_000 {
		t.Fatalf("7200s = %d ticks, want 7200000", sec)
	}
	if ToMillis(sec) != 7_200_000 {
		t.Fatalf("round trip millis mismatch: %d", ToMillis(sec))
	}
	// RTP clock at 90 kHz: 90 RTP units == 1 tick.
	if got := FromRTP(90_000, 90_000); got != 1000 {
		t.Fatalf("FromRTP(90000,90000) = %d, want 1000", got)
	}
	if got := FromRTP(480, 48_000); got != 10 {
		t.Fatalf("FromRTP(480,48000) = %d, want 10", got)
	}
	if got := FromRTP(1234, 0); got != 0 {
		t.Fatalf("zero clock rate = %d, want 0", got)
	}
}

func TestCheckedArithmetic(t *testing.T) {
	if _, err := CheckedAdd(math.MaxInt64, 1); err == nil {
		t.Fatal("max+1 must overflow")
	}
	if _, err := CheckedAdd(math.MinInt64, -1); err == nil {
		t.Fatal("min-1 must underflow")
	}
	if v, err := CheckedAdd(math.MaxInt64, 0); err != nil || v != math.MaxInt64 {
		t.Fatalf("max+0 = %d, %v", v, err)
	}
	if _, err := CheckedMul(math.MaxInt64, 2); err == nil {
		t.Fatal("max*2 must overflow")
	}
	if v, err := CheckedMul(7, 6); err != nil || v != 42 {
		t.Fatalf("7*6 = %d, %v", v, err)
	}
}

func TestTimelineMonotonicAndNegative(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Append(Point{At: 10, Track: "v", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tl.Append(Point{At: 20, Track: "v", Seq: 2}); err != nil {
		t.Fatal(err)
	}
	if err := tl.Append(Point{At: 20, Track: "v", Seq: 3}); err == nil {
		t.Fatal("duplicate tick must be rejected")
	} else if me, ok := As(err); !ok || me.Kind != KindNonMonotonic {
		t.Fatalf("want KindNonMonotonic, got %v", err)
	}
	if err := tl.Append(Point{At: -1, Track: "v"}); err == nil {
		t.Fatal("negative tick must be rejected")
	} else if me, ok := As(err); !ok || me.Kind != KindNegativeTime {
		t.Fatalf("want KindNegativeTime, got %v", err)
	}
	// Independent track restarts its own cursor.
	if err := tl.Append(Point{At: 5, Track: "a"}); err != nil {
		t.Fatal(err)
	}
	if tl.Len() != 3 {
		t.Fatalf("len = %d, want 3", tl.Len())
	}
}

func TestTimelinePointsAreCopied(t *testing.T) {
	tl := NewTimeline()
	_ = tl.Append(Point{At: 1, Track: "v"})
	pts := tl.Points()
	pts[0].At = 999
	again := tl.Points()
	if again[0].At != 1 {
		t.Fatal("mutating returned slice leaked into timeline")
	}
}

func TestRangeAndCrossTrackError(t *testing.T) {
	r := Range{Start: 100, End: 250}
	if r.DurationMillis() != 150 {
		t.Fatalf("duration = %d, want 150", r.DurationMillis())
	}
	a := []Point{{At: 10, Track: "v"}, {At: 30, Track: "v"}, {At: 61, Track: "v"}}
	b := []Point{{At: 11, Track: "a"}, {At: 29, Track: "a"}, {At: 60, Track: "a"}}
	if got := CrossTrackError(a, b); got != 1 {
		t.Fatalf("cross-track error = %d, want 1", got)
	}
}

func TestMediaErrorWrapping(t *testing.T) {
	err := NewError(KindOverlap, "manifest", "v0", 1500, "edits overlap")
	me, ok := As(err)
	if !ok || me.Kind != KindOverlap || me.Track != "v0" || me.AtTick != 1500 {
		t.Fatalf("unwrapped error: %v", err)
	}
	wrapped := errors.New("outer: " + err.Error())
	if _, ok := As(wrapped); ok {
		t.Fatal("string-wrapped error must not masquerade as MediaError")
	}
}
