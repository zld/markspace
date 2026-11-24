package clock

import "testing"

func TestToNanosRoundTrip(t *testing.T) {
	tb, err := NewTimebase(48000)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := tb.ToNanos(48000 * 3)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 3*NanosPerSecond {
		t.Fatalf("got %d want %d", ns, 3*NanosPerSecond)
	}
	back, err := tb.FromNanos(ns)
	if err != nil {
		t.Fatal(err)
	}
	if back != 144000 {
		t.Fatalf("round trip got %d", back)
	}
}

func TestToNanosRejectsNegativeAndOverflow(t *testing.T) {
	tb, _ := NewTimebase(90000)
	if _, err := tb.ToNanos(1 << 62); AsKind(err) != KindOverflow {
		t.Fatalf("overflow kind = %q err=%v", AsKind(err), err)
	}
	if _, err := NewTimebase(0); AsKind(err) != KindInvalidMetadata {
		t.Fatalf("zero timescale kind = %q", AsKind(err))
	}
}

func TestUnwrapSeries(t *testing.T) {
	values := []uint32{4294967290, 4294967295, 2, 7}
	got, err := UnwrapSeries(values, Mod32)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{4294967290, 4294967295, 4294967298, 4294967303}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d got %d want %d", i, got[i], want[i])
		}
	}
}

func TestUnwrapNearestReference(t *testing.T) {
	got, err := Unwrap(5, int64(Mod32)+1, Mod32)
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(Mod32)+5 {
		t.Fatalf("got %d want %d", got, int64(Mod32)+5)
	}
}

func TestIntervalErrors(t *testing.T) {
	if d, err := Duration(10, 20); err != nil || d != 10 {
		t.Fatalf("duration %d err %v", d, err)
	}
	if _, err := Duration(20, 20); err != nil {
		t.Fatalf("zero duration should be legal: %v", err)
	}
	if _, err := Duration(30, 20); AsKind(err) != KindNegativeDuration {
		t.Fatalf("kind = %q", AsKind(err))
	}
	err := New(KindOutOfRange, "crop exceeds frame").WithTrack("v1").WithLocation("box tkhd")
	if got := err.Error(); got != "track v1: out_of_range: crop exceeds frame at box tkhd" {
		t.Fatalf("error text = %q", got)
	}
	if !IsFatal(New(KindTruncated, "eof")) {
		t.Fatal("truncated must be fatal")
	}
	if IsFatal(New(KindNonMonotonic, "x")) {
		t.Fatal("non-monotonic should be isolatable")
	}
}
