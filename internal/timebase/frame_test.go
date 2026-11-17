package timebase

import (
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	r, err := ParseRate("24000/1001")
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 24000 || r.Den != 1001 {
		t.Fatalf("got %d/%d", r.Num, r.Den)
	}
	if _, err := ParseRate("25"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "abc", "0", "24/0", "-1/2"} {
		if _, err := ParseRate(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestMapHalfOpenRounding(t *testing.T) {
	r := Rate{Num: 24000, Den: 1001}
	// 5.005 s is exactly frame 120; 6 s is exactly frame 144.
	f, err := r.Map(5005*time.Millisecond, 6*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if f.Start != 120 || f.End != 144 {
		t.Fatalf("got [%d,%d), want [120,144)", f.Start, f.End)
	}
	// Start floors: 5.000 s -> frame 119 (boundary at 4.963s, next 5.005s).
	f, err = r.Map(5*time.Second, 6*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if f.Start != 119 || f.End != 144 {
		t.Fatalf("got [%d,%d), want [119,144)", f.Start, f.End)
	}
}

func TestMapRejectsBadIntervals(t *testing.T) {
	r := Rate{Num: 25, Den: 1}
	if _, err := r.Map(2*time.Second, 2*time.Second); err == nil {
		t.Fatal("expected zero duration error")
	}
	if _, err := r.Map(3*time.Second, 2*time.Second); err == nil {
		t.Fatal("expected reversed interval error")
	}
}

func TestFrameTimeRoundTrip(t *testing.T) {
	r := Rate{Num: 24000, Den: 1001}
	if got := r.FrameTime(120); got != 5005*time.Millisecond {
		t.Fatalf("frame 120 at %s, want 5.005s", got)
	}
	f, err := r.Map(5005*time.Millisecond, 7*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.FrameTime(f.Start); got != 5005*time.Millisecond {
		t.Fatalf("round trip start = %s", got)
	}
}

func TestRounding(t *testing.T) {
	if RoundMillis(1500*time.Microsecond) != 2 {
		t.Fatal("half should round up")
	}
	if RoundCentis(15*time.Millisecond) != 2 {
		t.Fatal("centi half should round up")
	}
	if MillisDuration(5005) != 5005*time.Millisecond {
		t.Fatal("MillisDuration")
	}
	if CentisDuration(501) != 5010*time.Millisecond {
		t.Fatal("CentisDuration")
	}
}
